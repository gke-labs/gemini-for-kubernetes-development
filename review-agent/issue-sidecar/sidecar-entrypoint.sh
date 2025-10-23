#!/bin/sh

# wait for redis to be available
until redis-cli -h redis PING | grep PONG; do
  echo "Waiting for redis..."
  sleep 2
done

# find gemini output file in path
OUTPUT_FILE=`/workspaces/gemini-output.txt`

# wait for gemini output file to be not empty
while [ ! -s $OUTPUT_FILE]; do
  echo "Waiting for gemini output..."
  sleep 10
done

# for ever loop and update redis with gemini output
while true; do
  # read gemini output and store it in redis
  GEMINI_OUTPUT1=$(cat $OUTPUT_FILE)
  echo "waiting for changes in gemini output ..."
  sleep 30
  GEMINI_OUTPUT2=$(cat $OUTPUT_FILE)
  # check if gemini output has changed
  if [ "$GEMINI_OUTPUT1" != "$GEMINI_OUTPUT2" ]; then
    echo "Gemini output changed ..."
    redis-cli -h redis HSET issue:repo:${REPO}:issue:${ISSUEID}:handler:${HANDLER} "draft" "$GEMINI_OUTPUT2"
    echo "Gemini output stored in redis."
  fi
done