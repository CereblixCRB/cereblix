/* Multithreaded benchmark of the REFERENCE core (xmrig-cereblix nm_neuromorph_cg.c,
 * the production computed-goto hash with the original scalar fill_scratch).
 * Same threading/pinning harness as nmminer -bench, so the aggregate H/s is a
 * fair head-to-head vs nm_fast under identical full-load conditions. */
#define _GNU_SOURCE
#include "nm_neuromorph.h"
#include "nm_params.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdatomic.h>
#include <pthread.h>
#include <time.h>
#ifdef _WIN32
  #include <windows.h>
#else
  #include <unistd.h>
  #include <sched.h>
#endif

static _Atomic uint64_t g_h=0; static _Atomic int g_run=1;
static int g_threads; static uint64_t *g_ds; static nm_params g_params; static uint64_t g_height=6000;

static void pin(int cpu){
#ifdef _WIN32
    if(cpu<64) SetThreadAffinityMask(GetCurrentThread(),(DWORD_PTR)1<<cpu);
#else
    cpu_set_t s; CPU_ZERO(&s); CPU_SET(cpu,&s); pthread_setaffinity_np(pthread_self(),sizeof s,&s);
#endif
}
typedef struct{int id;}targ;
static void *worker(void*a){
    targ*t=a; pin(t->id);
    nm_ctx c; memset(&c,0,sizeof c); nm_ctx_init_shared(&c);
    c.params=g_params; nm_aes128_expand(&c.aes,c.params.aes_key);
    nm_ctx_attach_dataset(&c,g_ds,c.params.dataset_key);
    uint8_t hdr[NM_HEADER_LEN]; memset(hdr,0xab,sizeof hdr); uint8_t out[32];
    uint64_t ctr=(uint64_t)t->id*0x9E3779B97F4A7C15ULL+1;
    while(atomic_load(&g_run)){
        for(int b=0;b<8;b++) hdr[NM_NONCE_OFFSET+b]=(uint8_t)(ctr>>(8*b));
        nm_hash(&c,hdr,g_height,out); atomic_fetch_add(&g_h,1); ctr++;
    }
    return NULL;
}
static double mono(void){ struct timespec ts; clock_gettime(CLOCK_MONOTONIC,&ts); return ts.tv_sec+ts.tv_nsec/1e9; }

int main(int argc,char**argv){
    int secs=argc>1?atoi(argv[1]):10;
    g_threads=argc>2?atoi(argv[2]):4;
    uint8_t seed[32]; for(int i=0;i<32;i++) seed[i]=(uint8_t)(i*7+1);
    nm_derive_params(seed,&g_params);
    g_ds=malloc((size_t)NM_DATASET_BYTES); nm_build_dataset(g_ds,g_params.dataset_key);
    printf("REF BENCH: %d threads (cg core, original fill), %ds\n",g_threads,secs);
    targ*ta=calloc(g_threads,sizeof*ta); pthread_t*th=calloc(g_threads,sizeof*th);
    for(int i=0;i<g_threads;i++){ ta[i].id=i; pthread_create(&th[i],NULL,worker,&ta[i]); }
    struct timespec w={1,0}; nanosleep(&w,NULL); atomic_store(&g_h,0);
    double t0=mono(); struct timespec rs={secs,0}; nanosleep(&rs,NULL);
    uint64_t h=atomic_load(&g_h); double t1=mono(); atomic_store(&g_run,0);
    printf("RESULT: %.0f H/s aggregate  (%.1f H/s/thread)\n",h/(t1-t0),h/(t1-t0)/g_threads);
    return 0;
}
