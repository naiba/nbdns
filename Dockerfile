FROM alpine:latest
RUN apk --no-cache --no-progress add ca-certificates
WORKDIR /nbdns
COPY nbdns ./

VOLUME ["/nbdns/data"]
ENTRYPOINT ["/nbdns/nbdns"]
