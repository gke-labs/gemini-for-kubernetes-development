#!/bin/sh

# wait for redis to be available
until redis-cli -h redis PING | grep PONG; do
  echo "Waiting for redis..."
  sleep 2
done

# find gemini output file in path

# wait for gemini output file to be not empty
while [ ! -s /output.txt ]; do
  echo "Waiting for gemini output..."
  find /workspaces/*/ -name gemini-output.txt -exec cp {} /output.txt \;
  sleep 10
done

OUTPUT_FILE=`find /workspaces/*/ -name gemini-output.txt`

# for ever loop and update redis with gemini output
while true; do
  # read gemini output and store it in redis
  GEMINI_OUTPUT=$(cat $OUTPUT_FILE)
  # check if gemini output has changed
  if [ "$GEMINI_OUTPUT" != "$LAST_WRITTEN" ]; then
    echo "Gemini output changed ..."
    redis-cli -h redis HSET pr:repo:${REPO}:pr:${PRID} "draft" "$GEMINI_OUTPUT"
    echo "Gemini output stored in redis."
    LAST_WRITTEN=$GEMINI_OUTPUT
  fi
  echo "waiting for changes in gemini output ..."
  sleep 30
done