FROM alpine:3.21
RUN apk add --no-cache ca-certificates
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/localgcp /usr/local/bin/localgcp
EXPOSE 4443 8085 8086 8088 8089 8090
ENTRYPOINT ["localgcp"]
CMD ["up"]
