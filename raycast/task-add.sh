#!/usr/bin/env bash
# Required parameters:
# @raycast.schemaVersion 1
# @raycast.title Qi Task Add
# @raycast.mode compact
# @raycast.argument1 { "type": "text", "placeholder": "Task text" }

qi task add "$1"
