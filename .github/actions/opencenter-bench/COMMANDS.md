# Ready-to-run openCenter CLI commands, per environment

Generated from `/home/dzoan/openCenter-cli/bin/opencenter` — opencenter version: 0.0.1-b1907da.
80 commands. Every line below is runnable as written, once the
fixture for that environment exists.

Create the fixture for an environment first:

```bash
opencenter cluster init tb-openstack --org testbench --type openstack --no-keygen --no-sops-keygen
opencenter cluster init tb-vmware --org testbench --type vmware --no-keygen --no-sops-keygen
opencenter cluster init tb-baremetal --org testbench --type baremetal --no-keygen --no-sops-keygen
opencenter cluster init tb-kind --org testbench --type kind --no-keygen --no-sops-keygen
```


## OpenStack (`--type openstack`)

The default provider. Configuration and generation work offline; discovery, sync and online validation need credentials.

| Command | Stage | Task | Needs | Ready-to-run |
|---|---|---|---|---|
| `cluster` | operate | cluster | — | `opencenter cluster --help` |
| `cluster active` | operate | cluster | — | `opencenter cluster active` |
| `cluster backup` | operate | cluster | — | `opencenter cluster backup --help` |
| `cluster backup create` | operate | cluster | — | `opencenter cluster backup create tb-openstack` |
| `cluster backup delete` | operate | cluster | — | `opencenter cluster backup delete BACKUP_ID` |
| `cluster backup list` | operate | cluster | — | `opencenter cluster backup list testbench/tb-openstack` |
| `cluster backup restore` | operate | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster backup restore BACKUP_ID` |
| `cluster backup schedule` | operate | cluster | — | `opencenter cluster backup schedule testbench/tb-openstack --interval 24h` |
| `cluster configure` | configure | cluster | — | `opencenter cluster configure testbench/tb-openstack` |
| `cluster deploy` | deploy | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster deploy testbench/tb-openstack` |
| `cluster describe` | operate | cluster | — | `opencenter cluster describe testbench/tb-openstack` |
| `cluster destroy` | teardown | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster destroy testbench/tb-openstack` |
| `cluster doctor` | validate | cluster | — | `opencenter cluster doctor testbench/tb-openstack` |
| `cluster drift` | operate | cluster | — | `opencenter cluster drift --help` |
| `cluster edit` | configure | cluster | — | `opencenter cluster edit testbench/tb-openstack` |
| `cluster env` | operate | cluster | — | `opencenter cluster env testbench/tb-openstack` |
| `cluster export` | operate | cluster | — | `opencenter cluster export testbench/tb-openstack` |
| `cluster generate` | generate | cluster | — | `opencenter cluster generate testbench/tb-openstack` |
| `cluster import` | init | cluster | — | `opencenter cluster import --help` |
| `cluster import apply` | init | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster import apply --repo https://github.com/your-org/your-cluster.git` |
| `cluster import report` | init | cluster | — | `opencenter cluster import report --repo https://github.com/your-org/your-cluster.git` |
| `cluster import scan` | init | cluster | git | `opencenter cluster import scan --repo https://github.com/your-org/your-cluster.git` |
| `cluster init` | init | cluster | — | `opencenter cluster init tb-openstack --org testbench --type openstack --no-keygen --no-sops-keygen` |
| `cluster list` | operate | cluster | — | `opencenter cluster list` |
| `cluster lock` | operate | cluster | — | `opencenter cluster lock testbench/tb-openstack --reason 'testing'` |
| `cluster migrate-layout` | configure | cluster | — | `opencenter cluster migrate-layout --org testbench --dry-run` |
| `cluster normalize` | configure | cluster | — | `opencenter cluster normalize testbench/tb-openstack` |
| `cluster pool` | configure | cluster | — | `opencenter cluster pool --help` |
| `cluster pool add` | configure | cluster | — | `opencenter cluster pool add workers --count 2 --flavor m1.medium --cluster testbench/tb-openstack` |
| `cluster pool list` | configure | cluster | — | `opencenter cluster pool list --cluster testbench/tb-openstack` |
| `cluster pool remove` | configure | cluster | — | `opencenter cluster pool remove workers --cluster testbench/tb-openstack` |
| `cluster pool scale` | configure | cluster | — | `opencenter cluster pool scale workers --count 3 --cluster testbench/tb-openstack` |
| `cluster pool update` | configure | cluster | — | `opencenter cluster pool update workers --count 3 --cluster testbench/tb-openstack` |
| `cluster service` | configure | cluster | — | `opencenter cluster service --help` |
| `cluster service disable` | configure | cluster | — | `opencenter cluster service disable cert-manager --cluster testbench/tb-openstack` |
| `cluster service enable` | configure | cluster | — | `opencenter cluster service enable cert-manager --param email=admin@example.com --cluster testbench/tb-openstack` |
| `cluster service options` | configure | cluster | — | `opencenter cluster service options loki` |
| `cluster service status` | configure | cluster | — | `opencenter cluster service status --cluster testbench/tb-openstack` |
| `cluster set` | configure | cluster | — | `opencenter cluster set testbench/tb-openstack opencenter.meta.env=dev` |
| `cluster status` | operate | cluster | — | `opencenter cluster status testbench/tb-openstack` |
| `cluster sync` | generate | cluster | cloud credentials | `opencenter cluster sync --help` |
| `cluster sync openstack` | generate | cluster | — | `opencenter cluster sync openstack testbench/tb-openstack` |
| `cluster unlock` | teardown | cluster | — | `opencenter cluster unlock testbench/tb-openstack --reason 'testing'` |
| `cluster use` | operate | cluster | — | `opencenter cluster use testbench/tb-openstack --persistent` |
| `cluster validate` | validate | cluster | — | `opencenter cluster validate testbench/tb-openstack` |
| `plugins` | operate | plugins | — | `opencenter plugins` |
| `secrets` | operate | secrets | — | `opencenter secrets --help` |
| `secrets decrypt` | operate | secrets | sops and age | `opencenter secrets decrypt` |
| `secrets delete` | operate | secrets | — | `opencenter secrets delete SECRET_NAME` |
| `secrets describe` | operate | secrets | — | `opencenter secrets describe SECRET_NAME` |
| `secrets encrypt` | operate | secrets | sops and age | `opencenter secrets encrypt` |
| `secrets get` | operate | secrets | — | `opencenter secrets get SECRET_NAME --show` |
| `secrets keys` | operate | secrets | — | `opencenter secrets keys --help` |
| `secrets keys backup` | operate | secrets | — | `opencenter secrets keys backup` |
| `secrets keys check` | operate | secrets | — | `opencenter secrets keys check --cluster testbench/tb-openstack` |
| `secrets keys generate` | generate | secrets | — | `opencenter secrets keys generate` |
| `secrets keys revoke` | operate | secrets | — | `opencenter secrets keys revoke --cluster testbench/tb-openstack --key AGE_KEY_FINGERPRINT --dry-run` |
| `secrets keys rotate` | operate | secrets | sops and age | `opencenter secrets keys rotate --cluster testbench/tb-openstack --type age` |
| `secrets keys validate` | validate | secrets | — | `opencenter secrets keys validate` |
| `secrets list` | operate | secrets | — | `opencenter secrets list` |
| `secrets login` | operate | secrets | cloud credentials | `opencenter secrets login` |
| `secrets set` | configure | secrets | — | `opencenter secrets set SECRET_NAME --from-file /dev/null` |
| `secrets status` | operate | secrets | — | `opencenter secrets status` |
| `secrets sync` | generate | secrets | cloud credentials | `opencenter secrets sync testbench/tb-openstack` |
| `secrets validate` | validate | secrets | — | `opencenter secrets validate testbench/tb-openstack` |
| `settings` | operate | settings | — | `opencenter settings --help` |
| `settings edit` | configure | settings | — | `opencenter settings edit` |
| `settings explain` | operate | settings | — | `opencenter settings explain --help` |
| `settings explain cluster-defaults` | operate | settings | — | `opencenter settings explain cluster-defaults` |
| `settings get` | operate | settings | — | `opencenter settings get logging.level` |
| `settings ide` | operate | settings | — | `opencenter settings ide` |
| `settings path` | operate | settings | — | `opencenter settings path` |
| `settings reset` | operate | settings | — | `opencenter settings reset` |
| `settings set` | configure | settings | — | `opencenter settings set logging.level debug` |
| `settings view` | operate | settings | — | `opencenter settings view` |
| `shell-init` | operate | shell-init | — | `opencenter shell-init` |
| `version` | operate | version | — | `opencenter version` |

## VMware (`--type vmware`)

vSphere. Configuration and generation work offline; the rest needs a vCenter.

| Command | Stage | Task | Needs | Ready-to-run |
|---|---|---|---|---|
| `cluster` | operate | cluster | — | `opencenter cluster --help` |
| `cluster active` | operate | cluster | — | `opencenter cluster active` |
| `cluster backup` | operate | cluster | — | `opencenter cluster backup --help` |
| `cluster backup create` | operate | cluster | — | `opencenter cluster backup create tb-vmware` |
| `cluster backup delete` | operate | cluster | — | `opencenter cluster backup delete BACKUP_ID` |
| `cluster backup list` | operate | cluster | — | `opencenter cluster backup list testbench/tb-vmware` |
| `cluster backup restore` | operate | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster backup restore BACKUP_ID` |
| `cluster backup schedule` | operate | cluster | — | `opencenter cluster backup schedule testbench/tb-vmware --interval 24h` |
| `cluster configure` | configure | cluster | — | `opencenter cluster configure testbench/tb-vmware` |
| `cluster deploy` | deploy | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster deploy testbench/tb-vmware` |
| `cluster describe` | operate | cluster | — | `opencenter cluster describe testbench/tb-vmware` |
| `cluster destroy` | teardown | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster destroy testbench/tb-vmware` |
| `cluster doctor` | validate | cluster | — | `opencenter cluster doctor testbench/tb-vmware` |
| `cluster drift` | operate | cluster | — | `opencenter cluster drift --help` |
| `cluster drift detect` | operate | cluster | — | `opencenter cluster drift detect testbench/tb-vmware` |
| `cluster drift reconcile` | operate | cluster | kubectl, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster drift reconcile testbench/tb-vmware` |
| `cluster drift schedule` | operate | cluster | — | `opencenter cluster drift schedule testbench/tb-vmware --interval 24h` |
| `cluster edit` | configure | cluster | — | `opencenter cluster edit testbench/tb-vmware` |
| `cluster env` | operate | cluster | — | `opencenter cluster env testbench/tb-vmware` |
| `cluster export` | operate | cluster | — | `opencenter cluster export testbench/tb-vmware` |
| `cluster generate` | generate | cluster | — | `opencenter cluster generate testbench/tb-vmware` |
| `cluster import` | init | cluster | — | `opencenter cluster import --help` |
| `cluster import apply` | init | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster import apply --repo https://github.com/your-org/your-cluster.git` |
| `cluster import report` | init | cluster | — | `opencenter cluster import report --repo https://github.com/your-org/your-cluster.git` |
| `cluster import scan` | init | cluster | git | `opencenter cluster import scan --repo https://github.com/your-org/your-cluster.git` |
| `cluster init` | init | cluster | — | `opencenter cluster init tb-vmware --org testbench --type vmware --no-keygen --no-sops-keygen` |
| `cluster list` | operate | cluster | — | `opencenter cluster list` |
| `cluster lock` | operate | cluster | — | `opencenter cluster lock testbench/tb-vmware --reason 'testing'` |
| `cluster migrate-layout` | configure | cluster | — | `opencenter cluster migrate-layout --org testbench --dry-run` |
| `cluster normalize` | configure | cluster | — | `opencenter cluster normalize testbench/tb-vmware` |
| `cluster pool` | configure | cluster | — | `opencenter cluster pool --help` |
| `cluster pool add` | configure | cluster | — | `opencenter cluster pool add workers --count 2 --flavor m1.medium --cluster testbench/tb-vmware` |
| `cluster pool list` | configure | cluster | — | `opencenter cluster pool list --cluster testbench/tb-vmware` |
| `cluster pool remove` | configure | cluster | — | `opencenter cluster pool remove workers --cluster testbench/tb-vmware` |
| `cluster pool scale` | configure | cluster | — | `opencenter cluster pool scale workers --count 3 --cluster testbench/tb-vmware` |
| `cluster pool update` | configure | cluster | — | `opencenter cluster pool update workers --count 3 --cluster testbench/tb-vmware` |
| `cluster service` | configure | cluster | — | `opencenter cluster service --help` |
| `cluster service disable` | configure | cluster | — | `opencenter cluster service disable cert-manager --cluster testbench/tb-vmware` |
| `cluster service enable` | configure | cluster | — | `opencenter cluster service enable cert-manager --param email=admin@example.com --cluster testbench/tb-vmware` |
| `cluster service options` | configure | cluster | — | `opencenter cluster service options loki` |
| `cluster service status` | configure | cluster | — | `opencenter cluster service status --cluster testbench/tb-vmware` |
| `cluster set` | configure | cluster | — | `opencenter cluster set testbench/tb-vmware opencenter.meta.env=dev` |
| `cluster status` | operate | cluster | — | `opencenter cluster status testbench/tb-vmware` |
| `cluster unlock` | teardown | cluster | — | `opencenter cluster unlock testbench/tb-vmware --reason 'testing'` |
| `cluster use` | operate | cluster | — | `opencenter cluster use testbench/tb-vmware --persistent` |
| `cluster validate` | validate | cluster | — | `opencenter cluster validate testbench/tb-vmware` |
| `plugins` | operate | plugins | — | `opencenter plugins` |
| `secrets` | operate | secrets | — | `opencenter secrets --help` |
| `secrets decrypt` | operate | secrets | sops and age | `opencenter secrets decrypt` |
| `secrets delete` | operate | secrets | — | `opencenter secrets delete SECRET_NAME` |
| `secrets describe` | operate | secrets | — | `opencenter secrets describe SECRET_NAME` |
| `secrets encrypt` | operate | secrets | sops and age | `opencenter secrets encrypt` |
| `secrets get` | operate | secrets | — | `opencenter secrets get SECRET_NAME --show` |
| `secrets keys` | operate | secrets | — | `opencenter secrets keys --help` |
| `secrets keys backup` | operate | secrets | — | `opencenter secrets keys backup` |
| `secrets keys check` | operate | secrets | — | `opencenter secrets keys check --cluster testbench/tb-vmware` |
| `secrets keys generate` | generate | secrets | — | `opencenter secrets keys generate` |
| `secrets keys revoke` | operate | secrets | — | `opencenter secrets keys revoke --cluster testbench/tb-vmware --key AGE_KEY_FINGERPRINT --dry-run` |
| `secrets keys rotate` | operate | secrets | sops and age | `opencenter secrets keys rotate --cluster testbench/tb-vmware --type age` |
| `secrets keys validate` | validate | secrets | — | `opencenter secrets keys validate` |
| `secrets list` | operate | secrets | — | `opencenter secrets list` |
| `secrets login` | operate | secrets | cloud credentials | `opencenter secrets login` |
| `secrets set` | configure | secrets | — | `opencenter secrets set SECRET_NAME --from-file /dev/null` |
| `secrets status` | operate | secrets | — | `opencenter secrets status` |
| `secrets sync` | generate | secrets | cloud credentials | `opencenter secrets sync testbench/tb-vmware` |
| `secrets validate` | validate | secrets | — | `opencenter secrets validate testbench/tb-vmware` |
| `settings` | operate | settings | — | `opencenter settings --help` |
| `settings edit` | configure | settings | — | `opencenter settings edit` |
| `settings explain` | operate | settings | — | `opencenter settings explain --help` |
| `settings explain cluster-defaults` | operate | settings | — | `opencenter settings explain cluster-defaults` |
| `settings get` | operate | settings | — | `opencenter settings get logging.level` |
| `settings ide` | operate | settings | — | `opencenter settings ide` |
| `settings path` | operate | settings | — | `opencenter settings path` |
| `settings reset` | operate | settings | — | `opencenter settings reset` |
| `settings set` | configure | settings | — | `opencenter settings set logging.level debug` |
| `settings view` | operate | settings | — | `opencenter settings view` |
| `shell-init` | operate | shell-init | — | `opencenter shell-init` |
| `version` | operate | version | — | `opencenter version` |

## Bare metal (`--type baremetal`)

Physical hosts from an inventory. No cloud API, so it is configuration and generation.

| Command | Stage | Task | Needs | Ready-to-run |
|---|---|---|---|---|
| `cluster` | operate | cluster | — | `opencenter cluster --help` |
| `cluster active` | operate | cluster | — | `opencenter cluster active` |
| `cluster backup` | operate | cluster | — | `opencenter cluster backup --help` |
| `cluster backup create` | operate | cluster | — | `opencenter cluster backup create tb-baremetal` |
| `cluster backup delete` | operate | cluster | — | `opencenter cluster backup delete BACKUP_ID` |
| `cluster backup list` | operate | cluster | — | `opencenter cluster backup list testbench/tb-baremetal` |
| `cluster backup restore` | operate | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster backup restore BACKUP_ID` |
| `cluster backup schedule` | operate | cluster | — | `opencenter cluster backup schedule testbench/tb-baremetal --interval 24h` |
| `cluster configure` | configure | cluster | — | `opencenter cluster configure testbench/tb-baremetal` |
| `cluster deploy` | deploy | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster deploy testbench/tb-baremetal` |
| `cluster describe` | operate | cluster | — | `opencenter cluster describe testbench/tb-baremetal` |
| `cluster destroy` | teardown | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster destroy testbench/tb-baremetal` |
| `cluster doctor` | validate | cluster | — | `opencenter cluster doctor testbench/tb-baremetal` |
| `cluster drift` | operate | cluster | — | `opencenter cluster drift --help` |
| `cluster edit` | configure | cluster | — | `opencenter cluster edit testbench/tb-baremetal` |
| `cluster env` | operate | cluster | — | `opencenter cluster env testbench/tb-baremetal` |
| `cluster export` | operate | cluster | — | `opencenter cluster export testbench/tb-baremetal` |
| `cluster generate` | generate | cluster | — | `opencenter cluster generate testbench/tb-baremetal` |
| `cluster import` | init | cluster | — | `opencenter cluster import --help` |
| `cluster import apply` | init | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster import apply --repo https://github.com/your-org/your-cluster.git` |
| `cluster import report` | init | cluster | — | `opencenter cluster import report --repo https://github.com/your-org/your-cluster.git` |
| `cluster import scan` | init | cluster | git | `opencenter cluster import scan --repo https://github.com/your-org/your-cluster.git` |
| `cluster init` | init | cluster | — | `opencenter cluster init tb-baremetal --org testbench --type baremetal --no-keygen --no-sops-keygen` |
| `cluster list` | operate | cluster | — | `opencenter cluster list` |
| `cluster lock` | operate | cluster | — | `opencenter cluster lock testbench/tb-baremetal --reason 'testing'` |
| `cluster migrate-layout` | configure | cluster | — | `opencenter cluster migrate-layout --org testbench --dry-run` |
| `cluster normalize` | configure | cluster | — | `opencenter cluster normalize testbench/tb-baremetal` |
| `cluster pool` | configure | cluster | — | `opencenter cluster pool --help` |
| `cluster pool add` | configure | cluster | — | `opencenter cluster pool add workers --count 2 --flavor m1.medium --cluster testbench/tb-baremetal` |
| `cluster pool list` | configure | cluster | — | `opencenter cluster pool list --cluster testbench/tb-baremetal` |
| `cluster pool remove` | configure | cluster | — | `opencenter cluster pool remove workers --cluster testbench/tb-baremetal` |
| `cluster pool scale` | configure | cluster | — | `opencenter cluster pool scale workers --count 3 --cluster testbench/tb-baremetal` |
| `cluster pool update` | configure | cluster | — | `opencenter cluster pool update workers --count 3 --cluster testbench/tb-baremetal` |
| `cluster service` | configure | cluster | — | `opencenter cluster service --help` |
| `cluster service disable` | configure | cluster | — | `opencenter cluster service disable cert-manager --cluster testbench/tb-baremetal` |
| `cluster service enable` | configure | cluster | — | `opencenter cluster service enable cert-manager --param email=admin@example.com --cluster testbench/tb-baremetal` |
| `cluster service options` | configure | cluster | — | `opencenter cluster service options loki` |
| `cluster service status` | configure | cluster | — | `opencenter cluster service status --cluster testbench/tb-baremetal` |
| `cluster set` | configure | cluster | — | `opencenter cluster set testbench/tb-baremetal opencenter.meta.env=dev` |
| `cluster status` | operate | cluster | — | `opencenter cluster status testbench/tb-baremetal` |
| `cluster unlock` | teardown | cluster | — | `opencenter cluster unlock testbench/tb-baremetal --reason 'testing'` |
| `cluster use` | operate | cluster | — | `opencenter cluster use testbench/tb-baremetal --persistent` |
| `cluster validate` | validate | cluster | — | `opencenter cluster validate testbench/tb-baremetal` |
| `plugins` | operate | plugins | — | `opencenter plugins` |
| `secrets` | operate | secrets | — | `opencenter secrets --help` |
| `secrets decrypt` | operate | secrets | sops and age | `opencenter secrets decrypt` |
| `secrets delete` | operate | secrets | — | `opencenter secrets delete SECRET_NAME` |
| `secrets describe` | operate | secrets | — | `opencenter secrets describe SECRET_NAME` |
| `secrets encrypt` | operate | secrets | sops and age | `opencenter secrets encrypt` |
| `secrets get` | operate | secrets | — | `opencenter secrets get SECRET_NAME --show` |
| `secrets keys` | operate | secrets | — | `opencenter secrets keys --help` |
| `secrets keys backup` | operate | secrets | — | `opencenter secrets keys backup` |
| `secrets keys check` | operate | secrets | — | `opencenter secrets keys check --cluster testbench/tb-baremetal` |
| `secrets keys generate` | generate | secrets | — | `opencenter secrets keys generate` |
| `secrets keys revoke` | operate | secrets | — | `opencenter secrets keys revoke --cluster testbench/tb-baremetal --key AGE_KEY_FINGERPRINT --dry-run` |
| `secrets keys rotate` | operate | secrets | sops and age | `opencenter secrets keys rotate --cluster testbench/tb-baremetal --type age` |
| `secrets keys validate` | validate | secrets | — | `opencenter secrets keys validate` |
| `secrets list` | operate | secrets | — | `opencenter secrets list` |
| `secrets login` | operate | secrets | cloud credentials | `opencenter secrets login` |
| `secrets set` | configure | secrets | — | `opencenter secrets set SECRET_NAME --from-file /dev/null` |
| `secrets status` | operate | secrets | — | `opencenter secrets status` |
| `secrets sync` | generate | secrets | cloud credentials | `opencenter secrets sync testbench/tb-baremetal` |
| `secrets validate` | validate | secrets | — | `opencenter secrets validate testbench/tb-baremetal` |
| `settings` | operate | settings | — | `opencenter settings --help` |
| `settings edit` | configure | settings | — | `opencenter settings edit` |
| `settings explain` | operate | settings | — | `opencenter settings explain --help` |
| `settings explain cluster-defaults` | operate | settings | — | `opencenter settings explain cluster-defaults` |
| `settings get` | operate | settings | — | `opencenter settings get logging.level` |
| `settings ide` | operate | settings | — | `opencenter settings ide` |
| `settings path` | operate | settings | — | `opencenter settings path` |
| `settings reset` | operate | settings | — | `opencenter settings reset` |
| `settings set` | configure | settings | — | `opencenter settings set logging.level debug` |
| `settings view` | operate | settings | — | `opencenter settings view` |
| `shell-init` | operate | shell-init | — | `opencenter shell-init` |
| `version` | operate | version | — | `opencenter version` |

## Kind (`--type kind`)

Local Kubernetes in containers. The only provider whose whole lifecycle runs here for nothing.

| Command | Stage | Task | Needs | Ready-to-run |
|---|---|---|---|---|
| `cluster` | operate | cluster | — | `opencenter cluster --help` |
| `cluster active` | operate | cluster | — | `opencenter cluster active` |
| `cluster backup` | operate | cluster | — | `opencenter cluster backup --help` |
| `cluster backup create` | operate | cluster | — | `opencenter cluster backup create tb-kind` |
| `cluster backup delete` | operate | cluster | — | `opencenter cluster backup delete BACKUP_ID` |
| `cluster backup list` | operate | cluster | — | `opencenter cluster backup list testbench/tb-kind` |
| `cluster backup restore` | operate | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster backup restore BACKUP_ID` |
| `cluster backup schedule` | operate | cluster | — | `opencenter cluster backup schedule testbench/tb-kind --interval 24h` |
| `cluster deploy` | deploy | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster deploy testbench/tb-kind` |
| `cluster describe` | operate | cluster | — | `opencenter cluster describe testbench/tb-kind` |
| `cluster destroy` | teardown | cluster | the provider, and the mutation gate, OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster destroy testbench/tb-kind` |
| `cluster doctor` | validate | cluster | — | `opencenter cluster doctor testbench/tb-kind` |
| `cluster drift` | operate | cluster | — | `opencenter cluster drift --help` |
| `cluster edit` | configure | cluster | — | `opencenter cluster edit testbench/tb-kind` |
| `cluster env` | operate | cluster | — | `opencenter cluster env testbench/tb-kind` |
| `cluster export` | operate | cluster | — | `opencenter cluster export testbench/tb-kind` |
| `cluster generate` | generate | cluster | — | `opencenter cluster generate testbench/tb-kind` |
| `cluster import` | init | cluster | — | `opencenter cluster import --help` |
| `cluster import apply` | init | cluster | OPENCLI_ALLOW_MUTATE=1 | `opencenter cluster import apply --repo https://github.com/your-org/your-cluster.git` |
| `cluster import report` | init | cluster | — | `opencenter cluster import report --repo https://github.com/your-org/your-cluster.git` |
| `cluster import scan` | init | cluster | git | `opencenter cluster import scan --repo https://github.com/your-org/your-cluster.git` |
| `cluster init` | init | cluster | — | `opencenter cluster init tb-kind --org testbench --type kind --no-keygen --no-sops-keygen` |
| `cluster list` | operate | cluster | — | `opencenter cluster list` |
| `cluster lock` | operate | cluster | — | `opencenter cluster lock testbench/tb-kind --reason 'testing'` |
| `cluster migrate-layout` | configure | cluster | — | `opencenter cluster migrate-layout --org testbench --dry-run` |
| `cluster normalize` | configure | cluster | — | `opencenter cluster normalize testbench/tb-kind` |
| `cluster pool` | configure | cluster | — | `opencenter cluster pool --help` |
| `cluster pool add` | configure | cluster | — | `opencenter cluster pool add workers --count 2 --flavor m1.medium --cluster testbench/tb-kind` |
| `cluster pool list` | configure | cluster | — | `opencenter cluster pool list --cluster testbench/tb-kind` |
| `cluster pool remove` | configure | cluster | — | `opencenter cluster pool remove workers --cluster testbench/tb-kind` |
| `cluster pool scale` | configure | cluster | — | `opencenter cluster pool scale workers --count 3 --cluster testbench/tb-kind` |
| `cluster pool update` | configure | cluster | — | `opencenter cluster pool update workers --count 3 --cluster testbench/tb-kind` |
| `cluster service` | configure | cluster | — | `opencenter cluster service --help` |
| `cluster service disable` | configure | cluster | — | `opencenter cluster service disable cert-manager --cluster testbench/tb-kind` |
| `cluster service enable` | configure | cluster | — | `opencenter cluster service enable cert-manager --param email=admin@example.com --cluster testbench/tb-kind` |
| `cluster service options` | configure | cluster | — | `opencenter cluster service options loki` |
| `cluster service status` | configure | cluster | — | `opencenter cluster service status --cluster testbench/tb-kind` |
| `cluster set` | configure | cluster | — | `opencenter cluster set testbench/tb-kind opencenter.meta.env=dev` |
| `cluster status` | operate | cluster | — | `opencenter cluster status testbench/tb-kind` |
| `cluster unlock` | teardown | cluster | — | `opencenter cluster unlock testbench/tb-kind --reason 'testing'` |
| `cluster use` | operate | cluster | — | `opencenter cluster use testbench/tb-kind --persistent` |
| `cluster validate` | validate | cluster | — | `opencenter cluster validate testbench/tb-kind` |
| `plugins` | operate | plugins | — | `opencenter plugins` |
| `secrets` | operate | secrets | — | `opencenter secrets --help` |
| `secrets decrypt` | operate | secrets | sops and age | `opencenter secrets decrypt` |
| `secrets delete` | operate | secrets | — | `opencenter secrets delete SECRET_NAME` |
| `secrets describe` | operate | secrets | — | `opencenter secrets describe SECRET_NAME` |
| `secrets encrypt` | operate | secrets | sops and age | `opencenter secrets encrypt` |
| `secrets get` | operate | secrets | — | `opencenter secrets get SECRET_NAME --show` |
| `secrets keys` | operate | secrets | — | `opencenter secrets keys --help` |
| `secrets keys backup` | operate | secrets | — | `opencenter secrets keys backup` |
| `secrets keys check` | operate | secrets | — | `opencenter secrets keys check --cluster testbench/tb-kind` |
| `secrets keys generate` | generate | secrets | — | `opencenter secrets keys generate` |
| `secrets keys revoke` | operate | secrets | — | `opencenter secrets keys revoke --cluster testbench/tb-kind --key AGE_KEY_FINGERPRINT --dry-run` |
| `secrets keys rotate` | operate | secrets | sops and age | `opencenter secrets keys rotate --cluster testbench/tb-kind --type age` |
| `secrets keys validate` | validate | secrets | — | `opencenter secrets keys validate` |
| `secrets list` | operate | secrets | — | `opencenter secrets list` |
| `secrets login` | operate | secrets | cloud credentials | `opencenter secrets login` |
| `secrets set` | configure | secrets | — | `opencenter secrets set SECRET_NAME --from-file /dev/null` |
| `secrets status` | operate | secrets | — | `opencenter secrets status` |
| `secrets sync` | generate | secrets | cloud credentials | `opencenter secrets sync testbench/tb-kind` |
| `secrets validate` | validate | secrets | — | `opencenter secrets validate testbench/tb-kind` |
| `settings` | operate | settings | — | `opencenter settings --help` |
| `settings edit` | configure | settings | — | `opencenter settings edit` |
| `settings explain` | operate | settings | — | `opencenter settings explain --help` |
| `settings explain cluster-defaults` | operate | settings | — | `opencenter settings explain cluster-defaults` |
| `settings get` | operate | settings | — | `opencenter settings get logging.level` |
| `settings ide` | operate | settings | — | `opencenter settings ide` |
| `settings path` | operate | settings | — | `opencenter settings path` |
| `settings reset` | operate | settings | — | `opencenter settings reset` |
| `settings set` | configure | settings | — | `opencenter settings set logging.level debug` |
| `settings view` | operate | settings | — | `opencenter settings view` |
| `shell-init` | operate | shell-init | — | `opencenter shell-init` |
| `version` | operate | version | — | `opencenter version` |

## Notes

- `PROVIDER` in a `cluster init` line is the `--type` for that section.
- Commands marked `OPENCLI_ALLOW_MUTATE=1` create or destroy real things;
  the bench refuses them without that variable set.
- A command whose *Needs* column is not satisfied still runs — it should
  fail with an explanation, and that failure is itself worth testing.
