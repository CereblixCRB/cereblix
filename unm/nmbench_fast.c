/* Benchmark + correctness harness for nm_fast.
 * 1) single-lane hash must equal the consensus VECTOR.
 * 2) batch (K-lane) must equal single-lane for the same headers.
 * 3) sweep K and report H/s/thread so we can pick the best lane count. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <time.h>
#include "nm_fast.h"
#include "nm_neuromorph.h"
#include "nm_params.h"

#define HDR NM_HEADER_LEN

static double now_s(void){ struct timespec t; clock_gettime(CLOCK_MONOTONIC,&t); return t.tv_sec + t.tv_nsec/1e9; }
static void hexout(const char*tag,const uint8_t*h){ printf("%s",tag); for(int i=0;i<32;i++) printf("%02x",h[i]); printf("\n"); }

int main(int argc,char**argv){
    int N      = argc>1?atoi(argv[1]):400;   /* hashes per measurement */
    uint64_t h = argc>2?strtoull(argv[2],0,10):6000;
    int maxK   = argc>3?atoi(argv[3]):8;
    if(maxK>NM_FAST_MAXLANES) maxK=NM_FAST_MAXLANES;

    uint8_t seed[32]; for(int i=0;i<32;i++) seed[i]=(uint8_t)(i*7+1);
    nm_epoch e; nm_fast_epoch_init(&e,seed);
    uint64_t *ds=(uint64_t*)malloc(NM_DATASET_BYTES);
    nm_fast_build_dataset(&e,ds); nm_fast_set_dataset(&e,ds);

    nm_lane lanes[NM_FAST_MAXLANES];
    for(int i=0;i<maxK;i++) if(nm_fast_lane_init(&lanes[i])){ printf("lane alloc fail\n"); return 1; }

    /* ---- correctness 1: VECTOR ---- */
    uint8_t hdr[HDR]; memset(hdr,0xab,sizeof hdr); memset(hdr+NM_NONCE_OFFSET,0,8);
    uint8_t out[32]; nm_fast_hash(&e,&lanes[0],hdr,h,out);
    hexout("VECTOR (single) : ",out);
    const char* EXPECT="a638d54f6702827833d2067eda741de9af70bb558fd560929d9905a87e37bc7a";
    char got[65]; for(int i=0;i<32;i++) sprintf(got+2*i,"%02x",out[i]);
    int vec_ok = (h==6000) ? (strcmp(got,EXPECT)==0) : 1;
    printf("VECTOR match    : %s\n", (h==6000)?(vec_ok?"YES":"NO!!!"):"(skip: h!=6000)");

    /* ---- correctness 2: batch == single for K lanes, varied nonces ---- */
    int batch_ok=1;
    {
        uint8_t H[NM_FAST_MAXLANES*HDR]; uint8_t ob[NM_FAST_MAXLANES*32], os[NM_FAST_MAXLANES*32];
        for(int L=0;L<maxK;L++){ memset(H+L*HDR,0xab,HDR); uint64_t nn=0x1111*L+7; memcpy(H+L*HDR+NM_NONCE_OFFSET,&nn,8); }
        nm_fast_hash_batch(&e,lanes,maxK,H,h,ob);
        for(int L=0;L<maxK;L++) nm_fast_hash(&e,&lanes[0],H+L*HDR,h,os+L*32);
        for(int L=0;L<maxK;L++) if(memcmp(ob+L*32,os+L*32,32)!=0){ batch_ok=0; printf("  batch mismatch lane %d\n",L); }
    }
    printf("batch==single   : %s\n\n", batch_ok?"YES":"NO!!!");
    if(!vec_ok||!batch_ok){ printf("CORRECTNESS FAILED\n"); return 2; }

    /* ---- speed: single lane ---- */
    double base=0;
    {
        double t0=now_s();
        for(int n=0;n<N;n++){ memcpy(hdr+NM_NONCE_OFFSET,&n,sizeof n); nm_fast_hash(&e,&lanes[0],hdr,h,out); }
        double s=now_s()-t0; base=N/s;
        printf("K=1 (single)    : %8.1f H/s/thread   (baseline)\n", base);
    }
    /* ---- speed: batch sweep ---- */
    for(int K=2;K<=maxK;K++){
        uint8_t H[NM_FAST_MAXLANES*HDR]; uint8_t ob[NM_FAST_MAXLANES*32];
        for(int L=0;L<K;L++) memset(H+L*HDR,0xab,HDR);
        int batches=N/K; if(batches<1) batches=1;
        double t0=now_s();
        for(int b=0;b<batches;b++){
            for(int L=0;L<K;L++){ uint64_t nn=(uint64_t)b*K+L; memcpy(H+L*HDR+NM_NONCE_OFFSET,&nn,8); }
            nm_fast_hash_batch(&e,lanes,K,H,h,ob);
        }
        double s=now_s()-t0; double hs=(double)batches*K/s;
        printf("K=%-2d (batch)    : %8.1f H/s/thread   x%.2f vs single\n", K, hs, hs/base);
    }
    return 0;
}
