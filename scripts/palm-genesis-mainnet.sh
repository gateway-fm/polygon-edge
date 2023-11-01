#!/bin/sh

apiKey="$1"

rm -f genesis.json

url=$(jq -r '.params.engine.polybft.bridge.jsonRPCEndpoint' genesis-mainnet.json)
url="$url$apiKey"

cat genesis-mainnet.json | jq ".params.engine.polybft.bridge.jsonRPCEndpoint = \"$url\"" > genesis.json
