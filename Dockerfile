FROM alpine:latest

RUN apk add curl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
RUN chmod +x kubectl
RUN mv ./kubectl /usr/local/bin/
RUN curl -LO https://github.com/kvaps/kubectl-node-shell/raw/master/kubectl-node_shell
RUN chmod +x ./kubectl-node_shell
RUN mv ./kubectl-node_shell /usr/local/bin/kubectl-node_shell