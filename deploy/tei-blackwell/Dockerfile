# TEI for Blackwell (RTX 50xx / sm_120).
#
# HuggingFace's `blackwell-latest` TEI image ships the sm_120 router binary but
# does NOT bundle the CUDA math libraries it links against (cuRAND, cuBLAS).
# We layer those in from an official CUDA 12.9 runtime image (12.8+ is required
# for Blackwell/sm_120 kernels). Result: batched GPU embeddings on the RTX 5070.
#
# Build (from repo root):
#   docker build -t ai-search-tei-blackwell:local deploy/tei-blackwell
# Selected via TEI_IMAGE in .env.
FROM nvidia/cuda:12.9.1-runtime-ubuntu24.04 AS cuda

FROM ghcr.io/huggingface/text-embeddings-inference:blackwell-latest

# cuRAND + cuBLAS (+ cuBLASLt dependency). COPY follows the SONAME symlinks and
# writes the concrete versioned libraries under these stable names.
COPY --from=cuda /usr/local/cuda/lib64/libcurand.so.10 /opt/cudalibs/
COPY --from=cuda /usr/local/cuda/lib64/libcublas.so.12 /opt/cudalibs/
COPY --from=cuda /usr/local/cuda/lib64/libcublasLt.so.12 /opt/cudalibs/

# The base image ships a CUDA *forward-compat* driver in /usr/local/cuda/compat
# (ahead of lib64 on LD_LIBRARY_PATH). Forward compat is only for data-center
# GPUs and fails with CUDA_ERROR_NO_DEVICE on consumer/WSL setups, shadowing the
# real driver the NVIDIA toolkit injects. Remove it so the injected driver wins.
RUN rm -f /usr/local/cuda*/compat/libcuda.so*

ENV LD_LIBRARY_PATH=/opt/cudalibs:${LD_LIBRARY_PATH}
