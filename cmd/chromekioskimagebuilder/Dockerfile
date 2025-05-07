FROM ubuntu:24.04 AS base

#############################

FROM base AS runtime

RUN \
        --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
        --mount=type=cache,target=/var/cache/apt,sharing=locked \
        rm -f /etc/apt/apt.conf.d/docker-clean \
        && apt-get update \
        && DEBIAN_FRONTEND="noninteractive" apt-get -yqq --no-install-recommends install \
            fatrace \
            fonts-dejavu-core \
            iproute2 \
            net-tools \
            strace \
            cage \
        ;

RUN \
        --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
        --mount=type=cache,target=/var/cache/apt,sharing=locked \
        DEBIAN_FRONTEND="noninteractive" apt-get -yqq --no-install-recommends install \
            ca-certificates \
            fonts-liberation \
            libasound2t64 \
            libatk-bridge2.0-0 \
            libatk1.0-0 \
            libatspi2.0-0 \
            libcairo2 \
            libcups2 \
            libcurl4 \
            libcurl4t64 \
            libgtk-3-0t64 \
            libnspr4 \
            libnss3 \
            libpango-1.0-0 \
            libxcomposite1 \
            libxdamage1 \
            libxkbcommon0 \
            wget \
            xdg-utils \
        ;

RUN \
	--mount=type=bind,source=./deb,dst=/tmp/deb \
	dpkg -i /tmp/deb/google-chrome-stable_current_amd64.deb

WORKDIR /work

#############################

FROM ubuntu:24.04 AS mkimage

RUN \
        --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
        --mount=type=cache,target=/var/cache/apt,sharing=locked \
        rm -f /etc/apt/apt.conf.d/docker-clean \
        && apt-get update \
        && DEBIAN_FRONTEND="noninteractive" apt-get -yqq --no-install-recommends install \
                squashfs-tools \
        && rm -rf /var/lib/apt/lists/* \
        ;

WORKDIR /work

RUN --mount=type=bind,target=/image,source=/,from=runtime \
        mksquashfs \
            /image \
            /work/image.squashfs \
            -noappend \
            -comp zstd \
        ;

###################################

FROM scratch AS dist

COPY --from=mkimage /work/image.squashfs /image.squashfs

CMD [""]
