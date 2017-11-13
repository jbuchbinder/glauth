FROM alpine:latest

COPY bin/glauth64      /app/glauth64
COPY sample-simple.cfg /data/glauth.cfg
COPY certs/server.key  /data/server.key
COPY certs/server.crt  /data/server.crt

EXPOSE 389
EXPOSE 636
EXPOSE 5555

VOLUME [ "/data" ]
CMD [ "/app/glauth64", "-c", "/data/glauth.cfg" ]
