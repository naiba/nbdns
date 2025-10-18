FROM alpine:latest
RUN apk --no-cache --no-progress add ca-certificates
WORKDIR /nbdns
COPY linux/${TARGETARCH}/nbdns ./

VOLUME ["/nbdns/data"]
ENTRYPOINT ["/nbdns/nbdns"]
