#export ARGUS_GITHUB_TOKEN=..
#export LOVELACE_GITHUB_TOKEN=..
#export HOPPER_GITHUB_TOKEN=..
#export ADA_GITHUB_TOKEN=..
#export REVIEWBOT_GITHUB_TOKEN=..
#export DAEDALUS_GITHUB_TOKEN=..
#export FEYNMAN_GITHUB_TOKEN=..
#export WALLE_GITHUB_TOKEN=..

alias factory=../factory/bin/factory

factory user onboard \
  --user argus-watcher-bot \
  --namespace overseer-system \
  --github-login argus-watcher-bot \
  --github-token $ARGUS_GITHUB_TOKEN \
  --github-email argus-watcher-bot@google.com \
  --gemini-key $GEMINI_API_KEY


factory user onboard \
  --user lovelace-coder-bot \
  --namespace overseer-system \
  --github-login lovelace-coder-bot \
  --github-token $LOVELACE_GITHUB_TOKEN \
  --github-email lovelace-coder-bot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user hopper-coder-bot \
  --namespace overseer-system \
  --github-login hopper-coder-bot \
  --github-token $HOPPER_GITHUB_TOKEN \
  --github-email hopper-coder-bot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user ada-coder-bot \
  --namespace overseer-system \
  --github-login ada-coder-bot \
  --github-token $ADA_GITHUB_TOKEN \
  --github-email ada-coder-bot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user reviewbot-robot \
  --namespace overseer-system \
  --github-login reviewbot-robot \
  --github-token $REVIEWBOT_GITHUB_TOKEN \
  --github-email reviewbot-robot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user daedalus-agent-bot \
  --namespace overseer-system \
  --github-login daedalus-agent-bot \
  --github-token $DAEDALUS_GITHUB_TOKEN \
  --github-email daedalus-agent-bot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user feynman-agent-bot \
  --namespace overseer-system \
  --github-login feynman-agent-bot \
  --github-token $FEYNMAN_GITHUB_TOKEN \
  --github-email feynman-agent-bot@google.com \
  --gemini-key $GEMINI_API_KEY

factory user onboard \
  --user walle-agent-bot \
  --namespace overseer-system \
  --github-login walle-agent-bot \
  --github-token $WALLE_GITHUB_TOKEN \
  --github-email walle-agent-bot@google.com \
  --gemini-key $GEMINI_API_KEY

