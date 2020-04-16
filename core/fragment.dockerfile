FROM debian:buster
ENV DEBIAN_FRONTEND=noninteractive
ENV FMT_VERSION 6.1.2
ENV ZMQ_VERSION 4.6.0

RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    libgnutls28-dev libcurl4-gnutls-dev \
    protobuf-compiler-grpc libprotobuf-dev libgrpc++-dev \
    pkg-config \
    wget \
    unzip \
    libzmq3-dev
RUN apt-get autoremove -y && apt-get clean -y && rm -rf /var/lib/apt/lists

WORKDIR /src/fmt
RUN wget -q https://github.com/fmtlib/fmt/releases/download/${FMT_VERSION}/fmt-${FMT_VERSION}.zip
RUN unzip fmt-${FMT_VERSION}.zip
RUN mkdir -p /src/fmt/fmt-${FMT_VERSION}/build
RUN cd fmt-${FMT_VERSION}/build
RUN cmake /src/fmt/fmt-${FMT_VERSION} -DCMAKE_BUILD_TYPE=Release -DFMT_TEST=OFF -DBUILD_SHARED_LIBS=ON
RUN make install
RUN rm -rf /src/fmt

WORKDIR /src/zmq
RUN wget https://github.com/zeromq/cppzmq/archive/v${ZMQ_VERSION}.zip
RUN unzip v${ZMQ_VERSION}.zip
RUN mkdir -p cppzmq-${ZMQ_VERSION}/build
RUN cd cppzmq-${ZMQ_VERSION}/build
RUN cmake /src/zmq/cppzmq-${ZMQ_VERSION} -DCMAKE_BUILD_TYPE=Release -DBUILD_TESTING=OFF -DBUILD_SHARED_LIBS=ON
RUN make install -j2
RUN rm -rf /src/zmq

WORKDIR /src
COPY core core
COPY protos protos
WORKDIR /build

WORKDIR /src/core/build
RUN cmake .. -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=ON -DBUILD_TESTING=OFF -DBUILD_PYTHON=OFF
RUN make -j4

CMD [ "./oneseismic-fragment", \
    "--account",    "$AZ_BLOB_ACCOUNT", \
    "--key",        "$AZ_BLOB_KEY", \
    "--source",     "$ONESEISMIC_MANIFEST_ADDRESS", \
    "--sink",       "$ONESEISMIC_SESSION_ADDRESS", \
    "--fail",       "$ONESEISMIC_FAIL_ADDRESS" \
]
