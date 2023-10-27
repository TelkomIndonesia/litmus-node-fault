#!/bin/sh

# Define the label key and value you want to search for
LABEL=${NODE_LABEL:-}

# Use `kubectl` to get the node name by label
NODE_NAMES=$(kubectl get nodes --selector="$LABEL" -o custom-columns=NAME:.metadata.name --no-headers)


if [ -z "$NODE_NAMES" ]; then
  echo "No nodes found with the label $LABEL_KEY=$LABEL_VALUE."
  exit 1
fi

for NODE_NAME in $NODE_NAMES; do
  echo "Running command on node: $NODE_NAME"
  kubectl node_shell $NODE_NAME -- reboot
done
