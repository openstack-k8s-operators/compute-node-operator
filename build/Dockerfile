FROM registry.access.redhat.com/ubi8/ubi-minimal:latest

ENV OPERATOR=/usr/local/bin/compute-node-operator \
    USER_UID=1001 \
    USER_NAME=compute-node-operator \
    OPERATOR_TEMPLATES=/usr/share/compute-node-operator/templates/

# install operator binary
COPY build/_output/bin/compute-node-operator ${OPERATOR}

COPY build/bin /usr/local/bin
COPY bindata /bindata
RUN  /usr/local/bin/user_setup

# install our templates
RUN  mkdir -p ${OPERATOR_TEMPLATES}
COPY templates ${OPERATOR_TEMPLATES}

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
