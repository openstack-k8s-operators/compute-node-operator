FROM registry.access.redhat.com/ubi8/ubi-minimal:latest

ENV OPERATOR=/usr/local/bin/compute-node-operator \
    USER_UID=1001 \
    USER_NAME=compute-node-operator \
    OPERATOR_TEMPLATES=/usr/share/compute-node-operator/templates/ \
    OPERATOR_BUNDLE=/usr/share/compute-node-operator/bundle/

# install operator binary
COPY build/_output/bin/compute-node-operator ${OPERATOR}
COPY build/_output/csv-generator /usr/local/bin/csv-generator

COPY build/bin /usr/local/bin
COPY bindata /bindata
RUN  /usr/local/bin/user_setup

# install our templates
RUN  mkdir -p ${OPERATOR_TEMPLATES}
COPY templates ${OPERATOR_TEMPLATES}

# install CRDs and required roles, services, etc
RUN  mkdir -p ${OPERATOR_BUNDLE}
COPY deploy/crds/*crd.yaml ${OPERATOR_BUNDLE}

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
