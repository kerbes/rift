#!/bin/bash

PORT=8080
EXISTING_PID=$(lsof -ti :"$PORT" 2>/dev/null)

if [ -n "$EXISTING_PID" ]; then
    echo "mockaws is already running on port $PORT (PID $EXISTING_PID)."
    read -rp "Kill it and restart? [y/N] " answer
    if [[ "$answer" =~ ^[Yy]$ ]]; then
        kill "$EXISTING_PID"
        sleep 1
    else
        echo "Leaving existing instance running."
        exit 0
    fi
fi

go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml
