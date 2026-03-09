FROM alpine:3.19

RUN apk add --no-cache inotify-tools redis

COPY acl-watcher.sh /usr/local/bin/acl-watcher
RUN chmod +x /usr/local/bin/acl-watcher

USER 1001:1001

ENTRYPOINT ["/usr/local/bin/acl-watcher"]
