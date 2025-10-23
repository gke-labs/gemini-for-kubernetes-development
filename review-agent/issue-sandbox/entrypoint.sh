#!/bin/bash

export GEMINI_API_KEY=`cat /tokens/gemini`
set -x
# .spec.source.cloneURL is typically upstream repo in a developer workflow
git remote remove origin

if [ "$GIT_PUSH_ENABLED" == "true" ]; then
  # if new origin is provided set it
  if [ ! -z $USER_ORIGIN ]; then
    git remote add origin $USER_ORIGIN
  fi
fi

if [ ! -z $USER_EMAIL ]; then
  git config --global user.email  $USER_EMAIL
fi

if [ ! -z "$USER_NAME" ]; then
  git config --global user.name "$USER_NAME"
fi

# grab the current git HEAD commit id
OLD_COMMIT_ID=$(git rev-parse HEAD)

# create the issue branch
git checkout -b $ISSUE_BRANCH

# if .gemini directory exists in /workspaces copy it to home directory
if [ -d /workspaces/.gemini ]; then
  echo ".gemini directory exists in /workspaces, copying to repo directory"
  # if desitation .gemini directory exists move it to .gemini.bak
  if [ -d .gemini ]; then
    echo ".gemini directory exists in repo directory, moving to .gemini.bak"
    mv .gemini .gemini.bak
  fi
  cp -R /workspaces/.gemini .gemini
else
  echo ".gemini directory does not exist in /workspaces"
fi

# protection against running gemini on an unpause
# if gemini-promt.txt dont run gemini else create and run it
if [ -f ../gemini-prompt.txt ]; then
  echo "gemini-prompt.txt exists, skipping gemini generation"
else
  echo "gemini-prompt.txt does not exist, running gemini"
  echo "$AGENT_PROMPT" > ../gemini-prompt.txt
  gemini -y  -p  "$AGENT_PROMPT" > ../gemini-output.txt || true
fi

rm -fr .gemini/
if [ -d .gemini.bak ]; then
  echo "moving .gemini.bak -> .gemini"
  mv .gemini.bak .gemini
fi

# Try commiting to grab uncommited changes
if [ ! -z $USER_EMAIL ]; then
  git add .
  git commit -m "fix for issue # $ISSUEID"  || true
fi


# grab new commit id
NEW_COMMIT_ID=$(git rev-parse HEAD)
if [ "$NEW_COMMIT_ID" != "$OLD_COMMIT_ID" ]; then
  echo "New changes committed"
  if [ "$GIT_PUSH_ENABLED" == "true" ]; then
    git push --set-upstream origin $ISSUE_BRANCH --force 
    echo "New changes pushed"
  fi
fi

#/usr/local/bin/code-server-entrypoint
/usr/bin/code-server --auth=none --bind-addr=0.0.0.0:13337