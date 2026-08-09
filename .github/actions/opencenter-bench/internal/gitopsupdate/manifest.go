package gitopsupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Updating the desired deployment state.
//
// Parsed and re-emitted through a YAML node tree, never rewritten with a
// regular expression. A regex over YAML cannot tell an image line in the
// container it means from an identical line in a sidecar, an initContainer, a
// commented-out block or a completely different document in the same file — and
// the failure mode is a manifest that still parses and deploys the wrong thing.
// Node editing also preserves comments, which matters because the file belongs
// to somebody else.
//
// Two layouts are supported, and which one applies is read from the file rather
// than configured:
//
//	Kustomization   images: [{name, newTag}]   — preferred
//	Deployment      spec.template.spec.containers[].image
//
// Kustomize's images: block is preferred because it states an intent — "this
// image, at this tag" — where a container image field states a string, and a
// reviewer can see the whole change on one line.

// ImageTag is the tag a tested commit is promoted as.
//
// sha- prefixed and never "latest". A mutable tag makes GitOps meaningless: the
// repository would record that some image was approved without recording which,
// and Flux would reconcile whatever happened to be behind the name that
// morning. PromoteImage refuses it outright rather than warning.
func ImageTag(sourceCommit string) string {
	short := ShortSHA(sourceCommit)
	if short == "" {
		return ""
	}
	return "sha-" + short
}

// ManifestChange is what an update did, for the report and for the diff check.
type ManifestChange struct {
	Path      string
	Kind      string // kustomization or deployment
	Container string
	Image     string
	Previous  string
	Changed   bool
}

// UpdateImage rewrites one manifest inside the checkout so it names the tested
// image.
//
// root is the checkout; relative is the configured manifest path. Both are
// checked rather than trusted: a path that escapes the checkout is the one
// mistake here that reaches outside the sandbox.
func UpdateImage(root, relative, imageRepository, tag, container string) (ManifestChange, error) {
	change := ManifestChange{Path: relative, Container: container}

	if strings.TrimSpace(imageRepository) == "" || strings.TrimSpace(tag) == "" {
		return change, fmt.Errorf("no image to promote: repository or tag is empty")
	}
	if tag == "latest" {
		return change, fmt.Errorf("refusing to promote the mutable tag %q: "+
			"GitOps records which build was approved, and \"latest\" records nothing", tag)
	}
	if err := checkRelative(relative); err != nil {
		return change, err
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := insideSandbox(root, path); err != nil {
		return change, fmt.Errorf("manifest path %q leaves the GitOps checkout", relative)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return change, fmt.Errorf("manifest path does not exist in the GitOps repository: %s", relative)
		}
		return change, err
	}

	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return change, fmt.Errorf("%s is not valid YAML: %w", relative, err)
	}
	if len(document.Content) == 0 {
		return change, fmt.Errorf("%s is empty", relative)
	}
	root_ := document.Content[0]

	change.Image = imageRepository + ":" + tag
	switch {
	case hasKey(root_, "images"):
		change.Kind = "kustomization"
		err = setKustomizeTag(root_, imageRepository, tag, &change)
	case hasKey(root_, "spec"):
		change.Kind = "deployment"
		err = setContainerImage(root_, container, change.Image, &change)
	default:
		return change, fmt.Errorf("%s has neither a kustomize images: block nor a "+
			"spec.template.spec.containers list — nothing here names an image", relative)
	}
	if err != nil {
		return change, err
	}
	if !change.Changed {
		// Already at the tested tag. Not an error — a re-run of the same commit
		// is a legitimate thing to do — but the caller needs to know there is
		// nothing to commit for this file.
		return change, nil
	}

	// Re-emitted at two-space indent, which is what every file in a Flux
	// repository already uses. A four-space rewrite would show as a diff on
	// every line and bury the one that changed.
	var out strings.Builder
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return change, fmt.Errorf("re-encode %s: %w", relative, err)
	}
	if err := encoder.Close(); err != nil {
		return change, fmt.Errorf("re-encode %s: %w", relative, err)
	}

	// Parse what is about to be written, not what was just held in memory. The
	// point is to catch an encoder that produced something the next reader
	// cannot load, and only re-parsing the bytes can catch that.
	var verify any
	if err := yaml.Unmarshal([]byte(out.String()), &verify); err != nil {
		return change, fmt.Errorf("the rewritten %s does not parse: %w", relative, err)
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		return change, err
	}
	return change, nil
}

// setKustomizeTag updates the one entry in images: whose name matches.
//
// Exactly one. Two entries for the same image is a repository-side mistake, and
// changing the first of them would leave the file self-contradictory in a way
// that is very hard to see in review.
func setKustomizeTag(root *yaml.Node, repository, tag string, change *ManifestChange) error {
	images := valueFor(root, "images")
	if images == nil || images.Kind != yaml.SequenceNode {
		return fmt.Errorf("images: is not a list")
	}
	var matched []*yaml.Node
	for _, entry := range images.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		name := valueFor(entry, "name")
		if name != nil && name.Value == repository {
			matched = append(matched, entry)
		}
	}
	switch len(matched) {
	case 0:
		return fmt.Errorf("no images: entry names %s — "+
			"add one before the bench can promote into it", repository)
	case 1:
	default:
		return fmt.Errorf("%d images: entries name %s; exactly one is required",
			len(matched), repository)
	}

	entry := matched[0]
	// newName is left alone deliberately. The stage promotes a tag; changing
	// which registry an image comes from is a different decision with different
	// reviewers.
	if existing := valueFor(entry, "newTag"); existing != nil {
		change.Previous = existing.Value
		if existing.Value == tag {
			return nil
		}
		existing.Value = tag
		existing.Tag = "!!str"
		existing.Style = 0
		change.Changed = true
		return nil
	}
	entry.Content = append(entry.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "newTag"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: tag})
	change.Changed = true
	return nil
}

// setContainerImage updates the named container's image in a Deployment.
func setContainerImage(root *yaml.Node, container, image string, change *ManifestChange) error {
	spec := valueFor(root, "spec")
	if spec == nil {
		return fmt.Errorf("no spec: in the manifest")
	}
	// spec.template.spec.containers, and only that. Not initContainers: a
	// bench promoting into an init container would be changing something it was
	// never asked about.
	template := valueFor(spec, "template")
	if template == nil {
		return fmt.Errorf("no spec.template: in the manifest")
	}
	podSpec := valueFor(template, "spec")
	if podSpec == nil {
		return fmt.Errorf("no spec.template.spec: in the manifest")
	}
	containers := valueFor(podSpec, "containers")
	if containers == nil || containers.Kind != yaml.SequenceNode {
		return fmt.Errorf("no spec.template.spec.containers list in the manifest")
	}

	var names []string
	for _, entry := range containers.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		name := valueFor(entry, "name")
		if name == nil {
			continue
		}
		names = append(names, name.Value)
		if name.Value != container {
			continue
		}
		imageNode := valueFor(entry, "image")
		if imageNode == nil {
			return fmt.Errorf("container %q has no image: field", container)
		}
		change.Previous = imageNode.Value
		if imageNode.Value == image {
			return nil
		}
		imageNode.Value = image
		imageNode.Tag = "!!str"
		imageNode.Style = 0
		change.Changed = true
		return nil
	}
	return fmt.Errorf("no container named %q in the manifest (it has: %s)",
		container, strings.Join(names, ", "))
}

// valueFor returns the value node for a key in a mapping.
func valueFor(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func hasKey(mapping *yaml.Node, key string) bool { return valueFor(mapping, key) != nil }

// WriteInto puts a file at a path inside the checkout, refusing anything that
// would leave it.
func WriteInto(root, relative string, content []byte) error {
	if err := checkRelative(relative); err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := insideSandbox(root, path); err != nil {
		return fmt.Errorf("path %q leaves the GitOps checkout", relative)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
