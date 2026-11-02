# In-cluster git:// remote for the offline exit.
#
# factory-m1-task:dev has the git client the task pods use. Alpine splits
# git-daemon into its own package, so this image only adds that package.
# The task image itself stays unchanged.
FROM factory-m1-task:dev

RUN apk add --no-cache git-daemon=2.54.0-r0
