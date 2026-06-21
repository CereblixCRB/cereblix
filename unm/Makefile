# nmminer — fast NeuroMorph CPU miner. Linux/Epyc build.
#
#   make            -> nmminer (the miner) + nmbench (offline benchmark uses nmminer -bench)
#   make ARCH=...   -> override -march (default: native; for a portable binary use x86-64-v3)
#   make tools      -> profilers + reference head-to-head benchmark
#
# Needs a CPU with AES-NI + AVX2 (any Zen/Haswell or newer). On Epyc, build ON the
# Epyc so -march=native enables Zen4/Zen5 codegen.

CC      ?= cc
ARCH    ?= native
CFLAGS  ?= -O3 -march=$(ARCH) -maes -mavx2 -funroll-loops -fno-tree-vectorize -ffp-contract=off -pthread
LDLIBS  := -lpthread -lm

CORE    := nm_fast.c nm_params.c

all: nmminer

nmminer: nmminer.c $(CORE) nm_fast.h nm_neuromorph.h nm_params.h nm_sha256.h nm_aes.h core_addr.h
	$(CC) $(CFLAGS) -o $@ nmminer.c $(CORE) $(LDLIBS)

# offline benchmark + correctness (VECTOR + batch==single)
nmbench_fast: nmbench_fast.c $(CORE)
	$(CC) $(CFLAGS) -o $@ nmbench_fast.c $(CORE) $(LDLIBS)

# reference (xmrig-cereblix computed-goto core) under the same harness, for honest A/B
nmref_mt: nmref_mt.c nm_neuromorph_cg.c nm_params.c
	$(CC) $(CFLAGS) -o $@ nmref_mt.c nm_neuromorph_cg.c nm_params.c $(LDLIBS)

tools: nmbench_fast nmref_mt

clean:
	rm -f nmminer nmbench_fast nmref_mt *.o

.PHONY: all tools clean
