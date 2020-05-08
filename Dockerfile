FROM golang:1.13 as build-env

WORKDIR /go/src/github.com/openstack-k8s-operators/compute-node-operator
ADD . .

RUN go build -o /compute-node-operator github.com/openstack-k8s-operators/compute-node-operator/cmd/manager

FROM registry.access.redhat.com/ubi8/ubi-minimal:latest

LABEL   name="compute-node-operator" \
        version="1.0" \
        summary="Compute Node Operator" \
        io.k8s.name="compute-node-operator" \
        io.k8s.description="This container includes the compute-node-operator"

ENV OPERATOR=/usr/local/bin/compute-node-operator \
    USER_UID=1001 \
    USER_NAME=compute-node-operator

# install operator binary
COPY --from=build-env /compute-node-operator ${OPERATOR}

COPY build/bin /usr/local/bin
COPY bindata /bindata
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
