cd /home/dzoan/opencenter/openclitestsimple
git add -A && git commit -m "Stage the bench from the action directory, not a second clone"
git push                     # your branch already tracks origin/main

cd /home/dzoan/opencenter/openCenter-cli-testDzoan
git commit --allow-empty -m "re-run the bench" && git push

