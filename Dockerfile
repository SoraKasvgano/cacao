FROM scratch
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
VOLUME /app/data
COPY cacao-${TARGETOS}-${TARGETARCH}${TARGETVARIANT} /usr/bin/cacao
ENTRYPOINT ["/usr/bin/cacao"]
CMD ["--storage=/app/data"]
