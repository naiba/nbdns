FROM alpine:latest
ARG TARGETPLATFORM
RUN apk --no-cache --no-progress add ca-certificates
WORKDIR /nbdns
COPY ${TARGETPLATFORM}/nbdns ./

VOLUME ["/nbdns/data"]
ENTRYPOINT ["/nbdns/nbdns"]
