#!/bin/sh

# Define the label key and value you want to search for
NODE_NAME="${7#*@}"
echo "$NODE_NAME"

LABEL=${NODE_LABEL:-}

# Use `kubectl` to get the node name by label
# NODE_NAMES=$(kubectl get nodes --selector="$LABEL" -o custom-columns=NAME:.metadata.name --no-headers)


# if [ -z "$NODE_NAMES" ]; then
#   echo "No nodes found with the label $LABEL_KEY=$LABEL_VALUE."
#   exit 1
# fi

# for NODE_NAME in $NODE_NAMES; do
#   kubectl config set-cluster kubernetes --certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt --server=https://kubernetes.default.svc
#   kubectl config set-credentials sa --token $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
#   kubectl config set-context default --cluster kubernetes --user=sa
#   kubectl config use-context default
#   echo "Running command on node: $NODE_NAME"
#   kubectl node_shell $NODE_NAME -- shutdown -r +3
# done

kubectl config set-cluster kubernetes --certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt --server=https://kubernetes.default.svc
kubectl config set-credentials sa --token $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)
kubectl config set-context default --cluster kubernetes --user=sa
kubectl config use-context default
echo "Running command on node: $NODE_NAME"
kubectl node_shell $NODE_NAME -- shutdown -r now