/* nmminer — standalone high-performance NeuroMorph CPU miner for Cereblix (CRB).
 *
 * Speaks the same HTTP getwork protocol as cereblix-miner (GET /getwork?addr=,
 * POST /submitwork) but uses the nm_fast core: optimized scratch fill + K-lane
 * dataset-interleave batching. ~1.6-2.1x the xmrig-cereblix fork per thread,
 * byte-identical hashes. Scales to many-core (Epyc 128T): one OS thread per core,
 * pinned, each running K interleaved nonce-lanes; shared per-epoch 64 MiB dataset.
 *
 * Connects to the SAME endpoints as the native miner: https:// pool/node URLs
 * (TLS handled by shelling out to curl) or plain http:// (raw sockets).
 */
#define _GNU_SOURCE
#include "nm_fast.h"
#include "nm_neuromorph.h"
#include "nm_params.h"
#include "nm_sha256.h"
#include "core_addr.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdatomic.h>
#include <pthread.h>
#include <time.h>
#include <errno.h>

#ifdef _WIN32
  #include <winsock2.h>
  #include <ws2tcpip.h>
  #include <windows.h>
  typedef SOCKET sock_t;
  #define CLOSESOCK closesocket
#else
  #include <sys/socket.h>
  #include <sys/mman.h>
  #include <netinet/in.h>
  #include <netinet/tcp.h>
  #include <arpa/inet.h>
  #include <netdb.h>
  #include <unistd.h>
  #include <sched.h>
  #include <sys/stat.h>
  #include <sys/wait.h>
  typedef int sock_t;
  #define INVALID_SOCKET (-1)
  #define CLOSESOCK close
#endif

/* portable monotonic clock + sleep (works under glibc/musl AND zig/mingw, so one
 * toolchain - zig - can cross-build Linux/HiveOS/Windows from the same source). */
#ifdef _WIN32
static double mono_s(void){ LARGE_INTEGER f,c; QueryPerformanceFrequency(&f); QueryPerformanceCounter(&c); return (double)c.QuadPart/(double)f.QuadPart; }
static void nm_sleep_ms(long ms){ Sleep((DWORD)(ms<0?0:ms)); }
#else
static double mono_s(void){ struct timespec ts; clock_gettime(CLOCK_MONOTONIC,&ts); return (double)ts.tv_sec + ts.tv_nsec/1e9; }
static void nm_sleep_ms(long ms){ struct timespec ts={ms/1000,(ms%1000)*1000000L}; nanosleep(&ts,NULL); }
#endif

/* ----------------------------- small HTTP/JSON ---------------------------- */

static int tcp_connect(const char *host, int port){
    char ports[16]; snprintf(ports,sizeof ports,"%d",port);
    struct addrinfo hints={0}, *res=NULL, *p;
    hints.ai_family=AF_UNSPEC; hints.ai_socktype=SOCK_STREAM;
    if(getaddrinfo(host,ports,&hints,&res)!=0) return -1;
    sock_t s=INVALID_SOCKET;
    for(p=res;p;p=p->ai_next){
        s=socket(p->ai_family,p->ai_socktype,p->ai_protocol);
        if(s==INVALID_SOCKET) continue;
        if(connect(s,p->ai_addr,(int)p->ai_addrlen)==0){ int one=1; setsockopt(s,IPPROTO_TCP,TCP_NODELAY,(const char*)&one,sizeof one); break; }
        CLOSESOCK(s); s=INVALID_SOCKET;
    }
    freeaddrinfo(res);
    return (int)s;
}

/* Minimal HTTP/1.1 request. Returns body (malloc'd, NUL-terminated) or NULL. */
static char *http_req(const char *host,int port,const char *method,const char *path,const char *body){
    int s=tcp_connect(host,port); if(s<0) return NULL;
    char req[2048]; int bl=body?(int)strlen(body):0;
    int n=snprintf(req,sizeof req,
        "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n"
        "Content-Type: application/json\r\nContent-Length: %d\r\n\r\n",
        method,path,host,bl);
    if(send(s,req,n,0)<0){ CLOSESOCK(s); return NULL; }
    if(bl) send(s,body,bl,0);
    size_t cap=8192,len=0; char *buf=malloc(cap);
    for(;;){ if(len+4096>cap){ cap*=2; buf=realloc(buf,cap); }
        int r=recv(s,buf+len,4096,0); if(r<=0) break; len+=r; }
    CLOSESOCK(s); buf[len]=0;
    char *bs=strstr(buf,"\r\n\r\n"); if(!bs){ free(buf); return NULL; }
    bs+=4; memmove(buf,bs,strlen(bs)+1);
    return buf;
}

/* ---- HTTPS via curl ------------------------------------------------------ */
/* nmminer has no built-in TLS; for https:// endpoints (the public pool/nodes,
 * exactly like the original native miner) we shell out to curl, which ships with
 * Windows 10+/Linux/HiveOS and handles TLS, Cloudflare and redirects. No linked
 * libssl -> none of the HiveOS libssl-crash risk. */
#ifdef _WIN32
  #define NM_POPEN _popen
  #define NM_PCLOSE _pclose
#else
  #define NM_POPEN popen
  #define NM_PCLOSE pclose
#endif
static _Atomic uint64_t g_tmpctr=0;
static const char *tmpdir_path(void){
#ifdef _WIN32
    const char *t=getenv("TEMP"); if(!t) t=getenv("TMP"); return t?t:".";
#else
    const char *t=getenv("TMPDIR"); return t?t:"/tmp";
#endif
}
static int curl_available(void){
#ifdef _WIN32
    return system("curl --version >NUL 2>&1")==0;
#else
    return system("curl --version >/dev/null 2>&1")==0;
#endif
}
/* run `curl` for a full URL; POST body (if any) goes through a temp file so no
 * JSON quoting can reach the shell. Returns malloc'd response body or NULL.
 * NOTE: -s (silent) but NOT -f: the pool answers rejected/stale submits with
 * HTTP 400 + a JSON body ({"error":...} / {"result":"stale"}); -f would turn
 * that into a raw "curl error 400" and drop the body. We read the body on any
 * status and let the JSON parser classify it. */
static char *curl_fetch(const char *method,const char *url,const char *body){
    char cmd[1400], bodyfile[300]={0};
    if(body && strcmp(method,"POST")==0){
        snprintf(bodyfile,sizeof bodyfile,"%s/.nmm_body_%llu.json",tmpdir_path(),
                 (unsigned long long)atomic_fetch_add(&g_tmpctr,1));
        FILE *bf=fopen(bodyfile,"wb"); if(bf){ fputs(body,bf); fclose(bf); }
        snprintf(cmd,sizeof cmd,
            "curl -s --max-time 20 -X POST -H \"Content-Type: application/json\" --data-binary @\"%s\" \"%s\"",
            bodyfile,url);
    } else {
        snprintf(cmd,sizeof cmd,"curl -s --max-time 20 \"%s\"",url);
    }
    FILE *fp=NM_POPEN(cmd,"r");
    if(!fp){ if(bodyfile[0]) remove(bodyfile); return NULL; }
    size_t cap=8192,len=0; char *buf=malloc(cap);
    for(;;){ size_t r=fread(buf+len,1,cap-len-1,fp); len+=r; if(r==0) break; if(len+1>=cap){ cap*=2; buf=realloc(buf,cap); } }
    NM_PCLOSE(fp); buf[len]=0;
    if(bodyfile[0]) remove(bodyfile);
    if(len==0){ free(buf); return NULL; }
    return buf;
}

/* extract a JSON string value: "key":"...."  -> dst (max n). returns 1 on hit. */
static int json_str(const char *j,const char *key,char *dst,int n){
    char pat[64]; snprintf(pat,sizeof pat,"\"%s\"",key);
    const char *p=strstr(j,pat); if(!p) return 0; p+=strlen(pat);
    while(*p&&*p!=':') p++; if(*p!=':') return 0; p++;
    while(*p==' '||*p=='\t') p++;
    if(*p!='"') return 0; p++;
    int i=0; while(*p&&*p!='"'&&i<n-1) dst[i++]=*p++; dst[i]=0; return 1;
}
/* extract a JSON number (or quoted number) -> uint64. returns 1 on hit. */
static int json_u64(const char *j,const char *key,uint64_t *out){
    char pat[64]; snprintf(pat,sizeof pat,"\"%s\"",key);
    const char *p=strstr(j,pat); if(!p) return 0; p+=strlen(pat);
    while(*p&&*p!=':') p++; if(*p!=':') return 0; p++;
    while(*p==' '||*p=='\t'||*p=='"') p++;
    if(*p<'0'||*p>'9') return 0; *out=strtoull(p,NULL,10); return 1;
}
static int json_bool_true(const char *j,const char *key){
    char pat[64]; snprintf(pat,sizeof pat,"\"%s\"",key);
    const char *p=strstr(j,pat); if(!p) return 0; p+=strlen(pat);
    while(*p&&*p!=':') p++; if(*p==':'){ p++; while(*p==' ')p++; return strncmp(p,"true",4)==0; }
    return 0;
}

static int hexbytes(const char *hex,uint8_t *out,int max){
    int n=(int)strlen(hex)/2; if(n>max) n=max;
    for(int i=0;i<n;i++){ unsigned v; if(sscanf(hex+2*i,"%2x",&v)!=1) return -1; out[i]=(uint8_t)v; }
    return n;
}

/* ----------------------------- allocation -------------------------------- */
/* huge-page-backed alloc on Linux (2 MiB pages cut TLB misses on the 64 MiB
 * dataset and 2 MiB scratchpads); transparent fallback to plain alloc. A small
 * registry records how each block was allocated so big_free releases it the
 * matching way (munmap vs _aligned_free vs free) - mixing them corrupts the heap. */
static struct { void *p; size_t sz; int mmapped; } g_balloc[16384];
static int g_balloc_n=0;
static pthread_mutex_t g_balloc_mtx=PTHREAD_MUTEX_INITIALIZER;
static void balloc_reg(void *p,size_t sz,int mmapped){
    pthread_mutex_lock(&g_balloc_mtx);
    if(g_balloc_n<(int)(sizeof g_balloc/sizeof g_balloc[0])){ int i=g_balloc_n++; g_balloc[i].p=p; g_balloc[i].sz=sz; g_balloc[i].mmapped=mmapped; }
    pthread_mutex_unlock(&g_balloc_mtx);
}
static void *big_alloc(size_t sz){
#ifndef _WIN32
    void *p=mmap(NULL,sz,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS|MAP_HUGETLB,-1,0);
    if(p!=MAP_FAILED){ balloc_reg(p,sz,1); return p; }
    p=mmap(NULL,sz,PROT_READ|PROT_WRITE,MAP_PRIVATE|MAP_ANONYMOUS,-1,0);
    if(p!=MAP_FAILED){
      #ifdef MADV_HUGEPAGE
        madvise(p,sz,MADV_HUGEPAGE);
      #endif
        balloc_reg(p,sz,1); return p;
    }
#endif
    void *q=NULL;
#ifdef _WIN32
    q=_aligned_malloc(sz,4096);
#else
    if(posix_memalign(&q,4096,sz)!=0) q=NULL;
#endif
    if(q) balloc_reg(q,sz,0);
    return q;
}
static void big_free(void *p){
    if(!p) return;
    int mmapped=0; size_t sz=0; int found=0;
    pthread_mutex_lock(&g_balloc_mtx);
    for(int i=0;i<g_balloc_n;i++) if(g_balloc[i].p==p){ mmapped=g_balloc[i].mmapped; sz=g_balloc[i].sz; g_balloc[i]=g_balloc[--g_balloc_n]; found=1; break; }
    pthread_mutex_unlock(&g_balloc_mtx);
    if(!found){ return; }
#ifndef _WIN32
    if(mmapped){ munmap(p,sz); return; }
    free(p);
#else
    (void)sz; (void)mmapped; _aligned_free(p);
#endif
}

/* --------------------------- CPU topology (auto) -------------------------- */
/* Auto-detect cores + NUMA so the miner self-tunes on ANY modern Intel/Ryzen/
 * EPYC with no flags: it pins one worker to each PHYSICAL core (NeuroMorph is
 * memory-bound, so SMT / Hyper-Threading siblings only thrash the shared L3 and
 * cut throughput), spreads the workers evenly across all NUMA nodes, and gives
 * each node its own local 64 MiB dataset copy (placed by first-touch from a
 * builder thread pinned to that node - no membind, works in restricted
 * containers). Override with -threads N / -smt / -nonuma. */
#define NM_MAXNODES 64
#define NM_MAXCPU   2048
#define NM_MAXPERNODE 1024

typedef struct { int cpu; int node; } nm_worker;
static int       g_nnodes = 1;
static nm_worker g_worker[NM_MAXCPU];
static int       g_nworker = 0;
static int       g_nlogical = 0, g_nphysical = 0;

static int default_threads(void);

/* pin the calling thread to one absolute logical CPU. */
static void pin_cpu(int cpu){
#ifdef _WIN32
    GROUP_AFFINITY ga; memset(&ga,0,sizeof ga);
    ga.Group=(WORD)(cpu/64); ga.Mask=(KAFFINITY)1<<(cpu%64);
    SetThreadGroupAffinity(GetCurrentThread(),&ga,NULL);
#else
    cpu_set_t set; CPU_ZERO(&set); CPU_SET(cpu,&set);
    pthread_setaffinity_np(pthread_self(),sizeof set,&set);
#endif
}

#ifndef _WIN32
static void parse_cpulist(const char *s,int node,int *cpu_node,int maxcpu){
    while(*s){
        while(*s==' '||*s=='\t') s++;
        if(*s<'0'||*s>'9') break;
        char *e; int a=(int)strtol(s,&e,10), b=a; s=e;
        if(*s=='-'){ s++; b=(int)strtol(s,&e,10); s=e; }
        for(int c=a;c<=b && c<maxcpu;c++) cpu_node[c]=node;
        if(*s==',') s++;
    }
}
/* Linux: NUMA node membership from the per-node sysfs cpulist, physical cores from
 * thread_siblings_list (a cpu is "primary"=physical if it is the lowest id among
 * its SMT siblings). */
static int detect_topo(int *cpu_node, unsigned char *primary, int maxcpu){
    int nlog=(int)sysconf(_SC_NPROCESSORS_ONLN); if(nlog<1)nlog=1; if(nlog>maxcpu)nlog=maxcpu;
    for(int c=0;c<maxcpu;c++){ cpu_node[c]=0; primary[c]=0; }
    int nnodes=0;
    for(int n=0;n<NM_MAXNODES;n++){
        char path[128]; snprintf(path,sizeof path,"/sys/devices/system/node/node%d/cpulist",n);
        FILE *f=fopen(path,"r"); if(!f) break;
        nnodes=n+1; char buf[8192]={0}; if(fgets(buf,sizeof buf,f)) parse_cpulist(buf,n,cpu_node,maxcpu);
        fclose(f);
    }
    if(nnodes<1) nnodes=1;
    for(int c=0;c<nlog;c++){
        char path[160]; snprintf(path,sizeof path,"/sys/devices/system/cpu/cpu%d/topology/thread_siblings_list",c);
        FILE *f=fopen(path,"r");
        if(!f){ primary[c]=1; continue; }   /* no topology info -> treat as physical */
        char buf[256]={0}; int first=c; if(fgets(buf,sizeof buf,f)) first=(int)strtol(buf,NULL,10);
        fclose(f);
        primary[c]=(first==c)?1:0;
    }
    g_nlogical=nlog;
    return nnodes;
}
#else
/* Windows: GetLogicalProcessorInformation -> physical cores (RelationProcessorCore,
 * primary = lowest bit of the core mask) + NUMA (RelationNumaNode). Covers the
 * common single-group (<=64 logical) case; falls back to all-logical/1-node. */
static int detect_topo(int *cpu_node, unsigned char *primary, int maxcpu){
    for(int c=0;c<maxcpu;c++){ cpu_node[c]=0; primary[c]=0; }
    DWORD len=0; GetLogicalProcessorInformation(NULL,&len);
    SYSTEM_LOGICAL_PROCESSOR_INFORMATION *buf=(SYSTEM_LOGICAL_PROCESSOR_INFORMATION*)malloc(len?len:1);
    int nlog=0, nnodes=1;
    if(buf && GetLogicalProcessorInformation(buf,&len)){
        int n=(int)(len/sizeof(*buf));
        for(int i=0;i<n;i++){
            ULONG_PTR m=buf[i].ProcessorMask;
            if(buf[i].Relationship==RelationProcessorCore){
                int first=-1;
                for(int b=0;b<64;b++) if(m&((ULONG_PTR)1<<b)){ if(first<0){first=b; if(b<maxcpu)primary[b]=1;} if(b+1>nlog)nlog=b+1; }
            } else if(buf[i].Relationship==RelationNumaNode){
                int nd=(int)buf[i].NumaNode.NodeNumber; if(nd+1>nnodes)nnodes=nd+1;
                for(int b=0;b<64;b++) if(m&((ULONG_PTR)1<<b)){ if(b<maxcpu) cpu_node[b]=nd; if(b+1>nlog)nlog=b+1; }
            }
        }
    }
    free(buf);
    if(nlog<1){ nlog=default_threads(); for(int c=0;c<nlog&&c<maxcpu;c++) primary[c]=1; }
    g_nlogical=nlog;
    return nnodes;
}
#endif

/* Build the worker plan: one worker per physical core (or every logical cpu if
 * use_smt), round-robin across NUMA nodes so a -threads cap still spreads over
 * all nodes. force_nonuma collapses everything to a single node. */
static void plan_workers(int use_smt, int cap, int force_nonuma){
    static int cpu_node[NM_MAXCPU]; static unsigned char primary[NM_MAXCPU];
    g_nnodes = detect_topo(cpu_node, primary, NM_MAXCPU);
    if(g_nnodes<1) g_nnodes=1; if(g_nnodes>NM_MAXNODES) g_nnodes=NM_MAXNODES;
    int nlog = g_nlogical>0?g_nlogical:default_threads(); if(nlog>NM_MAXCPU)nlog=NM_MAXCPU;
    if(force_nonuma){ g_nnodes=1; for(int c=0;c<nlog;c++) cpu_node[c]=0; }
    g_nphysical=0; for(int c=0;c<nlog;c++) if(primary[c]) g_nphysical++;
    if(g_nphysical<1){ g_nphysical=nlog; for(int c=0;c<nlog;c++) primary[c]=1; }

    static int nlist[NM_MAXNODES][NM_MAXPERNODE]; static int ncnt[NM_MAXNODES];
    for(int n=0;n<g_nnodes;n++) ncnt[n]=0;
    for(int c=0;c<nlog;c++){
        if(!(use_smt || primary[c])) continue;
        int nd=cpu_node[c]; if(nd<0||nd>=g_nnodes) nd=0;
        if(ncnt[nd]<NM_MAXPERNODE) nlist[nd][ncnt[nd]++]=c;
    }
    int maxcnt=0; for(int n=0;n<g_nnodes;n++) if(ncnt[n]>maxcnt) maxcnt=ncnt[n];
    g_nworker=0;
    for(int r=0;r<maxcnt && (cap<=0||g_nworker<cap);r++)
        for(int n=0;n<g_nnodes && (cap<=0||g_nworker<cap);n++)
            if(r<ncnt[n]){ g_worker[g_nworker].cpu=nlist[n][r]; g_worker[g_nworker].node=n; g_nworker++; }
    if(g_nworker<1){ g_worker[0].cpu=0; g_worker[0].node=0; g_nworker=1; }
}

/* ----------------------------- shared state ------------------------------ */

#define MAXNONCE_HDR NM_HEADER_LEN

static pthread_mutex_t g_mtx = PTHREAD_MUTEX_INITIALIZER;
static nm_epoch  g_epoch;                  /* params+aes (dataset set per-worker) */
static uint64_t *g_ds_cur[NM_MAXNODES] = {0};  /* current per-node 64 MiB datasets */
static uint64_t *g_ds_old[NM_MAXNODES] = {0};  /* retained one epoch deep for in-flight hashes */

/* alloc + build one 64 MiB dataset per USED node, each first-touched locally by a
 * builder thread pinned to a cpu of that node. out[node] gets the pointer (NULL
 * for unused nodes). Returns 0 on success. */
typedef struct { const nm_epoch *e; uint64_t *ds; int cpu; } nm_dsbuild;
static void *dsbuild_thread(void *a){ nm_dsbuild *b=(nm_dsbuild*)a; pin_cpu(b->cpu); nm_fast_build_dataset(b->e,b->ds); return NULL; }
static int build_node_datasets(const nm_epoch *e, uint64_t **out){
    static int rep[NM_MAXNODES]; static pthread_t th[NM_MAXNODES]; static int started[NM_MAXNODES];
    static nm_dsbuild args[NM_MAXNODES];
    for(int n=0;n<g_nnodes;n++){ rep[n]=-1; started[n]=0; out[n]=NULL; }
    for(int i=0;i<g_nworker;i++){ int n=g_worker[i].node; if(n>=0&&n<g_nnodes&&rep[n]<0) rep[n]=g_worker[i].cpu; }
    for(int n=0;n<g_nnodes;n++){
        if(rep[n]<0) continue;
        uint64_t *ds=big_alloc((size_t)NM_DATASET_BYTES);
        if(!ds){ for(int k=0;k<g_nnodes;k++) if(out[k]){ big_free(out[k]); out[k]=NULL; } return -1; }
        out[n]=ds; args[n].e=e; args[n].ds=ds; args[n].cpu=rep[n];
        if(pthread_create(&th[n],NULL,dsbuild_thread,&args[n])==0) started[n]=1;
        else { nm_fast_build_dataset(e,ds); }   /* fallback: build inline */
    }
    for(int n=0;n<g_nnodes;n++) if(started[n]) pthread_join(th[n],NULL);
    return 0;
}
static char      g_seed_hex[80] = "";
static uint8_t   g_header[NM_HEADER_LEN];
static uint8_t   g_target[32];
static uint64_t  g_height=0, g_epoch_no=0, g_extranonce=0;
static char      g_workid[256] = "";
static int       g_have_work=0;
static _Atomic uint64_t g_work_ver=0;
static _Atomic uint64_t g_hashes=0, g_shares=0, g_blocks=0;

static char g_host[256]; static int g_port; static char g_path[256]; static char g_addr[128];
static char g_base[512]; static int g_use_curl=0, g_have_curl=0;
static int g_lanes;

/* transport: 0 = HTTP getwork (legacy / direct node), 1 = Stratum (the default,
 * XMRig/Cryptonote dialect: login/job/submit, auto-reconnect). */
static int   g_is_stratum=0;
static char  g_strat_host[256]; static int g_strat_port=3333;
static sock_t g_strat_sock=INVALID_SOCKET;
static pthread_mutex_t g_strat_wmtx=PTHREAD_MUTEX_INITIALIZER;
static char  g_strat_session[80]="";
static _Atomic uint64_t g_submit_seq=100;

/* parse a pool/node URL into a transport:
 *   stratum+tcp://host:port  | stratum://host:port  | bare host:port  -> Stratum
 *   https://host/path  -> HTTP getwork via curl (TLS) ;  http://... -> raw sockets
 * Bare "host:port" defaults to Stratum (the recommended transport). */
static int parse_node(const char *url){
    snprintf(g_base,sizeof g_base,"%s",url);
    const char *p=url; int stratum=0;
    if(strncmp(url,"stratum+tcp://",14)==0){ p=url+14; stratum=1; }
    else if(strncmp(url,"stratum://",10)==0){ p=url+10; stratum=1; }
    else if(strncmp(url,"http://",7)==0){ p=url+7; g_use_curl=0; }
    else if(strncmp(url,"https://",8)==0){ p=url+8; g_use_curl=1; }
    else { stratum=1; }  /* bare host:port -> stratum */

    if(stratum){
        g_is_stratum=1;
        char hp[300]; snprintf(hp,sizeof hp,"%s",p);
        char *sl=strchr(hp,'/'); if(sl)*sl=0;
        char *colon=strrchr(hp,':');
        if(colon){ *colon=0; g_strat_port=atoi(colon+1); } else g_strat_port=3333;
        snprintf(g_strat_host,sizeof g_strat_host,"%s",hp);
        return 0;
    }
    const char *slash=strchr(p,'/');
    char hostport[300];
    if(slash){ size_t hl=slash-p; if(hl>=sizeof hostport)hl=sizeof hostport-1; memcpy(hostport,p,hl); hostport[hl]=0; snprintf(g_path,sizeof g_path,"%s",slash); }
    else { snprintf(hostport,sizeof hostport,"%s",p); strcpy(g_path,"/"); }
    char *colon=strchr(hostport,':');
    if(colon){ *colon=0; g_port=atoi(colon+1); } else g_port = g_use_curl?443:80;
    snprintf(g_host,sizeof g_host,"%s",hostport);
    return 0;
}

/* fetch a request relative to the base URL; suffix is appended (e.g.
 * "/getwork?addr=X" or "/submitwork"). Dispatches https->curl, http->sockets. */
static char *net_fetch(const char *method,const char *suffix,const char *body){
    if(g_use_curl){
        char url[700]; snprintf(url,sizeof url,"%s%s",g_base,suffix);
        return curl_fetch(method,url,body);
    }
    char path[700]; snprintf(path,sizeof path,"%s%s",g_path,suffix);
    return http_req(g_host,g_port,method,path,body);
}

/* Publish a unit of work (from getwork OR stratum). On a seed (epoch) change,
 * rebuild the per-NUMA-node datasets first, then swap everything under the lock
 * and bump the work version so miners pick it up. Shared by both transports. */
static void publish_work(const uint8_t header[NM_HEADER_LEN], const uint8_t target[32],
                         const uint8_t seed[32], const char *seed_hex,
                         uint64_t height, uint64_t epoch, uint64_t exn, const char *workid){
    int new_epoch = strcmp(seed_hex,g_seed_hex)!=0;
    nm_epoch ne;
    uint64_t *new_ds[NM_MAXNODES]; for(int n=0;n<NM_MAXNODES;n++) new_ds[n]=NULL;
    if(new_epoch){
        nm_fast_epoch_init(&ne,seed);
        if(height>=NM_DATASET_HEIGHT){
            fprintf(stderr,"[epoch %llu] building %d x 64 MiB dataset (one per NUMA node)...\n",
                    (unsigned long long)epoch,g_nnodes);
            if(build_node_datasets(&ne,new_ds)!=0){ fprintf(stderr,"dataset alloc/build failed\n"); return; }
        }
    }
    pthread_mutex_lock(&g_mtx);
    if(new_epoch){
        for(int n=0;n<g_nnodes;n++){ if(g_ds_old[n]) big_free(g_ds_old[n]); g_ds_old[n]=g_ds_cur[n]; g_ds_cur[n]=new_ds[n]; }
        g_epoch=ne; snprintf(g_seed_hex,sizeof g_seed_hex,"%s",seed_hex);
    }
    memcpy(g_header,header,NM_HEADER_LEN); memcpy(g_target,target,32);
    g_height=height; g_epoch_no=epoch; g_extranonce=exn;
    snprintf(g_workid,sizeof g_workid,"%s",workid?workid:"");
    g_have_work=1;
    atomic_fetch_add(&g_work_ver,1);
    pthread_mutex_unlock(&g_mtx);
}

/* fetch one HTTP getwork; rebuild epoch dataset if the seed changed; publish. */
static int fetch_work(void){
    char suffix[200]; snprintf(suffix,sizeof suffix,"/getwork?addr=%s",g_addr);
    char *body=net_fetch("GET",suffix,NULL);
    if(!body) return -1;
    char header_hex[2*NM_HEADER_LEN+4], target_hex[80], seed_hex[80], id[256];
    uint64_t epoch=0,height=0,exn=0;
    int ok = json_str(body,"header",header_hex,sizeof header_hex)
           && json_str(body,"target",target_hex,sizeof target_hex)
           && json_str(body,"seed",seed_hex,sizeof seed_hex);
    json_str(body,"id",id,sizeof id);
    json_u64(body,"epoch",&epoch); json_u64(body,"height",&height); json_u64(body,"extranonce",&exn);
    free(body);
    if(!ok) return -2;

    uint8_t header[NM_HEADER_LEN], target[32], seed[32];
    if(hexbytes(header_hex,header,NM_HEADER_LEN)!=NM_HEADER_LEN) return -3;
    int tn=hexbytes(target_hex,target,32); if(tn<0) return -3;
    if(tn<32){ memmove(target+(32-tn),target,tn); memset(target,0,32-tn); } /* right-align */
    if(hexbytes(seed_hex,seed,32)!=32) return -3;

    publish_work(header,target,seed,seed_hex,height,epoch,exn,id);
    return 0;
}

/* --------------------------------- stratum -------------------------------- */
/* one writer at a time on the shared socket */
static int strat_send_line(const char *s){
    int rc=-1, n=(int)strlen(s);
    pthread_mutex_lock(&g_strat_wmtx);
    if(g_strat_sock!=INVALID_SOCKET) rc=(send(g_strat_sock,s,n,0)==n)?0:-1;
    pthread_mutex_unlock(&g_strat_wmtx);
    return rc;
}
/* XMRig-style submit: 8 nonce bytes as little-endian hex (as they sit in the blob)
 * + the 32-byte result hash hex. The bridge re-pins our extranonce and the pool
 * re-verifies, so accept/reject arrives asynchronously on the read loop. */
static void stratum_submit(const char *jobid, uint64_t nonce, const uint8_t hash[32]){
    char noncehex[20]; for(int i=0;i<8;i++) sprintf(noncehex+2*i,"%02x",(unsigned)((nonce>>(8*i))&0xFFu));
    char rh[66]; for(int i=0;i<32;i++) sprintf(rh+2*i,"%02x",hash[i]);
    char body[700]; unsigned long long sid=atomic_fetch_add(&g_submit_seq,1);
    snprintf(body,sizeof body,
        "{\"id\":%llu,\"method\":\"submit\",\"params\":{\"id\":\"%s\",\"job_id\":\"%s\",\"nonce\":\"%s\",\"result\":\"%s\"}}\n",
        sid,g_strat_session,jobid,noncehex,rh);
    strat_send_line(body);
}

static void submit_share(const char *id,uint64_t nonce,uint64_t height,const uint8_t hash[32]){
    if(g_is_stratum){ stratum_submit(id,nonce,hash); return; }  /* result counted on the read loop */
    /* HTTP getwork submit (direct node / legacy). */
    char body[512]; snprintf(body,sizeof body,"{\"id\":\"%s\",\"nonce\":\"%llu\"}",id,(unsigned long long)nonce);
    char *resp=net_fetch("POST","/submitwork",body);
    if(!resp) return;
    char result[32]=""; json_str(resp,"result",result,sizeof result);
    char err[160]=""; json_str(resp,"error",err,sizeof err);
    if(err[0]) fprintf(stderr,"  submit block %llu rejected: %s\n",(unsigned long long)height,err);
    else if(strcmp(result,"share")==0){ atomic_fetch_add(&g_shares,1);
        if(json_bool_true(resp,"block")){ atomic_fetch_add(&g_blocks,1); fprintf(stderr,"  *** your share solved BLOCK %llu! ***\n",(unsigned long long)height); }
        else fprintf(stderr,"  share accepted\n");
    } else if(strcmp(result,"stale")!=0 && strcmp(result,"duplicate")!=0){
        atomic_fetch_add(&g_blocks,1); fprintf(stderr,"  *** BLOCK %llu FOUND & ACCEPTED ***\n",(unsigned long long)height);
    }
    free(resp);
}

/* parse a stratum job object (works on the whole login-result OR a job push line,
 * since we just scan for the fields) and publish it. */
static void strat_apply_job(const char *j){
    char blob_hex[2*NM_HEADER_LEN+8], seed_hex[80], target_hex[80], jobid[256]="";
    uint64_t height=0;
    if(!json_str(j,"blob",blob_hex,sizeof blob_hex)) return;
    if(!json_str(j,"seed_hash",seed_hex,sizeof seed_hex)) return;
    if(!json_str(j,"target",target_hex,sizeof target_hex)) return;
    json_str(j,"job_id",jobid,sizeof jobid);
    json_u64(j,"height",&height);
    uint8_t header[NM_HEADER_LEN], target[32]={0}, seed[32];
    if(hexbytes(blob_hex,header,NM_HEADER_LEN)!=NM_HEADER_LEN) return;
    int tn=hexbytes(target_hex,target,32); if(tn<0) return;
    if(tn<32){ memmove(target+(32-tn),target,tn); memset(target,0,32-tn); } /* right-align short targets */
    if(hexbytes(seed_hex,seed,32)!=32) return;
    uint64_t exn=(uint64_t)header[NM_NONCE_OFFSET+6] | ((uint64_t)header[NM_NONCE_OFFSET+7]<<8);
    publish_work(header,target,seed,seed_hex,height,height/NM_EPOCH_LENGTH,exn,jobid);
}

/* stratum client: connect, login, stream jobs, submit, auto-reconnect forever. */
static void *stratum_thread(void *arg){
    (void)arg;
    static char acc[65536];
    for(;;){
        int s=tcp_connect(g_strat_host,g_strat_port);
        if(s<0){ fprintf(stderr,"stratum: connect %s:%d failed - retrying\n",g_strat_host,g_strat_port); nm_sleep_ms(3000); continue; }
#ifdef _WIN32
        DWORD rto=5000; setsockopt(s,SOL_SOCKET,SO_RCVTIMEO,(const char*)&rto,sizeof rto);
#else
        struct timeval rto={5,0}; setsockopt(s,SOL_SOCKET,SO_RCVTIMEO,&rto,sizeof rto);
#endif
        pthread_mutex_lock(&g_strat_wmtx); g_strat_sock=s; pthread_mutex_unlock(&g_strat_wmtx);
        char login[400];
        snprintf(login,sizeof login,
            "{\"id\":1,\"method\":\"login\",\"params\":{\"login\":\"%s\",\"pass\":\"x\",\"agent\":\"unm/2.0\",\"algo\":[\"nm/1\",\"neuromorph\"]}}\n",g_addr);
        strat_send_line(login);
        fprintf(stderr,"stratum: connected %s:%d, logging in as %s...\n",g_strat_host,g_strat_port,g_addr);
        int alen=0; double last_ka=mono_s();
        for(;;){
            if(mono_s()-last_ka>45.0){ strat_send_line("{\"id\":2,\"method\":\"keepalived\",\"params\":{}}\n"); last_ka=mono_s(); }
            char tmp[8192];
            int r=recv(s,tmp,sizeof tmp,0);
            if(r==0) break;
            if(r<0){
#ifdef _WIN32
                int e=WSAGetLastError(); if(e==WSAETIMEDOUT||e==WSAEWOULDBLOCK) continue;
#else
                if(errno==EAGAIN||errno==EWOULDBLOCK||errno==EINTR) continue;
#endif
                break;
            }
            if(alen+r >= (int)sizeof acc-1) alen=0;             /* overflow guard */
            memcpy(acc+alen,tmp,r); alen+=r; acc[alen]=0;
            char *start=acc,*nl;
            while((nl=memchr(start,'\n',(size_t)(acc+alen-start)))){
                *nl=0;
                /* login result carries the session id at result.id - standard
                 * stratum servers/proxies authenticate submits by it (the cereblix
                 * bridge keys on job_id and ignores it, but xmrig-proxy requires it). */
                char *rp=strstr(start,"\"result\":{");
                if(rp){ char sess[80]=""; if(json_str(rp,"id",sess,sizeof sess) && sess[0]) snprintf(g_strat_session,sizeof g_strat_session,"%s",sess); }
                if(strstr(start,"\"blob\"")) strat_apply_job(start);
                else if(strstr(start,"\"error\"") && !strstr(start,"\"error\":null") && !strstr(start,"\"error\": null")){
                    char em[160]=""; json_str(start,"message",em,sizeof em);
                    fprintf(stderr,"  share rejected: %s\n",em[0]?em:"(stratum error)");
                } else if(strstr(start,"\"status\"") && strstr(start,"OK") && !strstr(start,"KEEPALIVED")){
                    atomic_fetch_add(&g_shares,1); fprintf(stderr,"  share accepted\n");
                }
                start=nl+1;
            }
            int rem=(int)(acc+alen-start); memmove(acc,start,rem); alen=rem; acc[alen]=0;
        }
        pthread_mutex_lock(&g_strat_wmtx); g_strat_sock=INVALID_SOCKET; CLOSESOCK(s); pthread_mutex_unlock(&g_strat_wmtx);
        /* invalidate work so miners pause until the new session delivers a fresh
         * job+extranonce (avoids "nonce not bound" rejects during the gap) */
        pthread_mutex_lock(&g_mtx); g_have_work=0; pthread_mutex_unlock(&g_mtx);
        atomic_fetch_add(&g_work_ver,1);
        fprintf(stderr,"stratum: disconnected - reconnecting in 2s\n");
        nm_sleep_ms(2000);
    }
    return NULL;
}

/* ----------------------------- threads ----------------------------------- */

typedef struct { int id; int cpu; int node; } targ;

static void *mine_thread(void *arg){
    targ *t=arg; int id=t->id; int mynode=t->node;
    pin_cpu(t->cpu);
    int K=g_lanes;
    nm_lane lanes[NM_FAST_MAXLANES];
    for(int i=0;i<K;i++){
        for(;;){    /* never give up a core on a transient alloc failure - retry */
            lanes[i].prog=malloc((size_t)NM_PROG_MAX*sizeof(nm_instr));
            lanes[i].taken=malloc(NM_PROG_MAX);
            lanes[i].scratch=big_alloc((size_t)NM_SCRATCH_WORDS*8);
            if(lanes[i].prog&&lanes[i].taken&&lanes[i].scratch) break;
            free(lanes[i].prog); lanes[i].prog=NULL; free(lanes[i].taken); lanes[i].taken=NULL;
            if(lanes[i].scratch){ big_free(lanes[i].scratch); lanes[i].scratch=NULL; }
            fprintf(stderr,"[UNM] worker %d: memory alloc retry (low memory?) - leave it running\n",id);
            nm_sleep_ms(1000);
        }
    }
    uint8_t H[NM_FAST_MAXLANES*NM_HEADER_LEN], obuf[NM_FAST_MAXLANES*32];

    const uint64_t enShift=48, threadShift=40;
    const uint64_t counterMask=((uint64_t)1<<threadShift)-1;
    uint64_t local_ver=0; nm_epoch le; uint8_t hdr[NM_HEADER_LEN],tgt[32];
    uint64_t height=0,exn=0; char id_str[256];
    int have=0;
    uint64_t ctr=(uint64_t)time(NULL)*2654435761u + id*0x9E3779B1u;

    for(;;){
        uint64_t v=atomic_load(&g_work_ver);
        if(v!=local_ver || !have){
            pthread_mutex_lock(&g_mtx);
            if(g_have_work){ le=g_epoch; le.dataset=g_ds_cur[mynode];
                memcpy(hdr,g_header,NM_HEADER_LEN); memcpy(tgt,g_target,32);
                height=g_height; exn=g_extranonce; snprintf(id_str,sizeof id_str,"%s",g_workid); have=1; }
            else have=0;   /* work invalidated (e.g. stratum dropped) -> pause, don't submit stale-extranonce shares */
            pthread_mutex_unlock(&g_mtx);
            local_ver=v;
        }
        if(!have){ nm_sleep_ms(200); continue; }
        /* dataset for this node not built yet (epoch switch in flight) - wait,
         * never dereference a NULL dataset (that would be the one real crash). */
        if(height>=NM_DATASET_HEIGHT && le.dataset==NULL){ nm_sleep_ms(200); continue; }

        uint64_t base=(exn&0xFFFF)<<enShift | ((uint64_t)(id&0xFF))<<threadShift;
        for(int L=0;L<K;L++) memcpy(H+L*NM_HEADER_LEN,hdr,NM_HEADER_LEN);

        /* run a chunk of batches before re-checking for new work */
        for(int chunk=0; chunk<64; chunk++){
            for(int L=0;L<K;L++){
                uint64_t nonce=base|((ctr+L)&counterMask);
                uint8_t *hh=H+L*NM_HEADER_LEN;
                for(int b=0;b<8;b++) hh[NM_NONCE_OFFSET+b]=(uint8_t)(nonce>>(8*b));
            }
            nm_fast_hash_batch(&le,lanes,K,H,height,obuf);
            atomic_fetch_add(&g_hashes,K);
            for(int L=0;L<K;L++){
                if(memcmp(obuf+L*32,tgt,32)<=0){
                    uint64_t nonce=base|((ctr+L)&counterMask);
                    submit_share(id_str,nonce,height,obuf+L*32);
                }
            }
            ctr+=K;
            if(atomic_load(&g_work_ver)!=local_ver) break;
        }
    }
    return NULL;
}

/* ---- offline benchmark mode (no network): pure aggregate H/s ---- */
static _Atomic int g_bench_run=1;
static void *bench_thread(void *arg){
    targ *t=arg; int id=t->id; pin_cpu(t->cpu);
    nm_epoch le=g_epoch; le.dataset=g_ds_cur[t->node];
    int K=g_lanes;
    nm_lane lanes[NM_FAST_MAXLANES];
    for(int i=0;i<K;i++){ lanes[i].prog=malloc((size_t)NM_PROG_MAX*sizeof(nm_instr));
        lanes[i].taken=malloc(NM_PROG_MAX); lanes[i].scratch=big_alloc((size_t)NM_SCRATCH_WORDS*8); }
    uint8_t H[NM_FAST_MAXLANES*NM_HEADER_LEN], obuf[NM_FAST_MAXLANES*32];
    for(int L=0;L<K;L++){ memset(H+L*NM_HEADER_LEN,0xab,NM_HEADER_LEN); }
    uint64_t ctr=(uint64_t)id*0x9E3779B97F4A7C15ULL+1;
    while(atomic_load(&g_bench_run)){
        for(int L=0;L<K;L++){ uint64_t nn=ctr+L; uint8_t*hh=H+L*NM_HEADER_LEN;
            for(int b=0;b<8;b++) hh[NM_NONCE_OFFSET+b]=(uint8_t)(nn>>(8*b)); }
        nm_fast_hash_batch(&le,lanes,K,H,g_height,obuf);
        atomic_fetch_add(&g_hashes,K); ctr+=K;
    }
    for(int i=0;i<K;i++){ free(lanes[i].prog); free(lanes[i].taken); big_free(lanes[i].scratch); }
    return NULL;
}
/* spin up g_nworker bench workers (pinned, node-local dataset) at lane-count K for
 * `secs`, return aggregate H/s */
static double bench_measure(int K,double secs){
    g_lanes=K; atomic_store(&g_bench_run,1); atomic_store(&g_hashes,0);
    int N=g_nworker;
    targ *ta=calloc(N,sizeof *ta); pthread_t *th=calloc(N,sizeof *th);
    for(int i=0;i<N;i++){ ta[i].id=i; ta[i].cpu=g_worker[i].cpu; ta[i].node=g_worker[i].node; pthread_create(&th[i],NULL,bench_thread,&ta[i]); }
    nm_sleep_ms(300); atomic_store(&g_hashes,0); /* warmup */
    double t0=mono_s();
    nm_sleep_ms((long)(secs*1000.0));
    uint64_t h=atomic_load(&g_hashes); double t1=mono_s();
    atomic_store(&g_bench_run,0);
    for(int i=0;i<N;i++) pthread_join(th[i],NULL);
    free(ta); free(th);
    return h/(t1-t0);
}
static void setup_test_epoch(void){
    uint8_t seed[32]; for(int i=0;i<32;i++) seed[i]=(uint8_t)(i*7+1);
    nm_fast_epoch_init(&g_epoch,seed);
    uint64_t *nd[NM_MAXNODES]; for(int n=0;n<NM_MAXNODES;n++) nd[n]=NULL;
    fprintf(stderr,"building %d x 64 MiB dataset (one per NUMA node)...\n",g_nnodes);
    build_node_datasets(&g_epoch,nd);
    for(int n=0;n<g_nnodes;n++){ if(g_ds_cur[n]) big_free(g_ds_cur[n]); g_ds_cur[n]=nd[n]; }
    g_height=6000;
}
static int run_bench(int seconds){
    setup_test_epoch();
    printf("BENCH: %d workers (%d NUMA node%s, phys %d/log %d) x %d lanes, %ds | fill %s\n",
           g_nworker,g_nnodes,g_nnodes==1?"":"s",g_nphysical,g_nlogical,g_lanes,seconds,nm_fast_fill_name());
    double hs=bench_measure(g_lanes,seconds);
    printf("RESULT: %.0f H/s aggregate  (%.1f H/s/thread)\n",hs,hs/(g_nworker?g_nworker:1));
    return 0;
}
/* pick the lane count that maximizes aggregate H/s on THIS machine under full
 * load. Tests a curated set up to maxK (big-cache EPYC can like 6-8; cache-tight
 * desktops want 1). */
static int autotune_lanes(int maxK){
    static const int cand[]={1,2,3,4,6,8,12,16};
    setup_test_epoch();
    printf("autotuning lanes on %d workers...\n",g_nworker);
    int best=1; double besths=0;
    for(int ci=0; ci<(int)(sizeof cand/sizeof cand[0]); ci++){
        int K=cand[ci]; if(K>maxK) break; if(K>NM_FAST_MAXLANES) break;
        double hs=bench_measure(K,2.5);
        printf("  lanes=%-2d : %.0f H/s%s\n",K,hs,hs>besths?"  *":"");
        if(hs>besths*1.02){ besths=hs; best=K; }   /* require >2% to prefer higher K */
    }
    printf("autotune -> lanes=%d (%.0f H/s)\n",best,besths);
    return best;
}

static void *fetch_thread(void *arg){
    (void)arg;
    for(;;){
        int rc=fetch_work();
        if(rc!=0){ static int warned=0; if(!warned){ fprintf(stderr,"getwork failed (rc=%d) - retrying, leave it running\n",rc); warned=1; } }
        nm_sleep_ms(3000);
    }
    return NULL;
}

/* ------------------------------ self-update ------------------------------- */
/* Same model as the production cereblix-miner: check a tiny manifest on GitHub
 * (then the cereblix.com origin as a RU-friendly fallback), and if a newer
 * version is published, download the platform binary, VERIFY its sha256 against
 * the manifest (nmminer already has SHA-256 - no extra deps, never a blind swap),
 * atomically swap it in (keeping a .old backup) and re-exec. Forward-only. Opt
 * out with -noupdate or a `.noupdate` file next to the binary. */
#define NM_VERSION "2.0"
static char **g_argv=NULL;
static char  g_exe_path[4096]="";
static const char *g_update_mirrors[]={
    "https://github.com/CereblixCRB/cereblix/releases/latest/download/",
    "https://cereblix.com/",
};
static const char *nm_plat_file(void){
#ifdef _WIN32
    return "unm-windows-amd64.exe";
#else
    return "unm-linux-amd64";
#endif
}
static const char *nm_plat_key(void){
#ifdef _WIN32
    return "windows-amd64";
#else
    return "linux-amd64";
#endif
}
static void nm_exe_path(char *out,size_t n){
#ifdef _WIN32
    DWORD r=GetModuleFileNameA(NULL,out,(DWORD)n); if(r==0||r>=n) out[0]=0;
#else
    ssize_t r=readlink("/proc/self/exe",out,n-1); if(r<=0){ out[0]=0; return; } out[r]=0;
#endif
}
/* a>b as dotted versions ("1.10" > "1.9"). */
static int nm_newer(const char *a,const char *b){
    while(*a||*b){
        int x=0,y=0;
        while(*a>='0'&&*a<='9'){ x=x*10+(*a-'0'); a++; }
        while(*b>='0'&&*b<='9'){ y=y*10+(*b-'0'); b++; }
        if(x!=y) return x>y;
        if(*a=='.')a++; if(*b=='.')b++;
        if(!*a&&!*b) break;
    }
    return 0;
}
static int nm_noupdate_optout(void){
    char exe[4096]; nm_exe_path(exe,sizeof exe); if(!exe[0]) return 0;
    char *sl=strrchr(exe,'/');
#ifdef _WIN32
    char *bs=strrchr(exe,'\\'); if(bs>sl) sl=bs;
#endif
    char p[4096];
    if(sl){ size_t d=(size_t)(sl-exe)+1; if(d>=sizeof p)d=sizeof p-1; memcpy(p,exe,d); snprintf(p+d,sizeof p-d,".noupdate"); }
    else snprintf(p,sizeof p,".noupdate");
    FILE *f=fopen(p,"rb"); if(f){ fclose(f); return 1; } return 0;
}
/* download url -> path via curl (handles TLS + GitHub redirects, binary-safe). */
static int nm_download_file(const char *url,const char *path){
    char cmd[1600];
#ifdef _WIN32
    snprintf(cmd,sizeof cmd,"curl -sL --max-time 300 -o \"%s\" \"%s\"",path,url);
#else
    snprintf(cmd,sizeof cmd,"curl -sL --max-time 300 -o \"%s\" \"%s\"",path,url);
#endif
    return system(cmd)==0?0:-1;
}
static long nm_filesize(const char *path){
    FILE *f=fopen(path,"rb"); if(!f) return -1; fseek(f,0,SEEK_END); long n=ftell(f); fclose(f); return n;
}
static int nm_file_sha256_hex(const char *path,char out[65]){
    FILE *f=fopen(path,"rb"); if(!f) return -1;
    fseek(f,0,SEEK_END); long n=ftell(f); fseek(f,0,SEEK_SET);
    if(n<=0){ fclose(f); return -1; }
    uint8_t *buf=malloc((size_t)n); if(!buf){ fclose(f); return -1; }
    size_t rd=fread(buf,1,(size_t)n,f); fclose(f);
    if(rd!=(size_t)n){ free(buf); return -1; }
    uint8_t h[32]; nm_sha256(buf,(size_t)n,h); free(buf);
    for(int i=0;i<32;i++) sprintf(out+2*i,"%02x",h[i]);
    return 0;
}
/* returns 1 if it updated (caller should re-exec), 0 up-to-date, -1 unreachable/err */
static int nm_check_and_update(int verbose){
    if(!g_have_curl) g_have_curl=curl_available();
    if(!g_have_curl){ if(verbose) fprintf(stderr,"update: curl not found on PATH - skipping\n"); return -1; }
    char exe[4096]; nm_exe_path(exe,sizeof exe); if(!exe[0]) return -1;
    /* NM_UPDATE_URL lets you point at a self-hosted update base (your own node) */
    const char *single[1]; const char **mirrors=g_update_mirrors;
    size_t nmir=sizeof g_update_mirrors/sizeof g_update_mirrors[0];
    const char *envurl=getenv("NM_UPDATE_URL");
    if(envurl&&envurl[0]){ single[0]=envurl; mirrors=single; nmir=1; }
    for(size_t mi=0; mi<nmir; mi++){
        char murl[700]; snprintf(murl,sizeof murl,"%sunm.manifest",mirrors[mi]);
        char *m=curl_fetch("GET",murl,NULL);
        if(!m) continue;
        char ver[40]=""; if(!json_str(m,"version",ver,sizeof ver) || !ver[0]){ free(m); continue; }
        if(!nm_newer(ver,NM_VERSION)){ free(m); if(verbose) fprintf(stderr,"update: up to date (v%s)\n",NM_VERSION); return 0; }
        char want_sha[80]=""; json_str(m,nm_plat_key(),want_sha,sizeof want_sha);
        free(m);
        if(verbose) fprintf(stderr,"update: v%s available (have v%s) - downloading...\n",ver,NM_VERSION);
        char burl[700]; snprintf(burl,sizeof burl,"%s%s",mirrors[mi],nm_plat_file());
        char newp[4112]; snprintf(newp,sizeof newp,"%s.new",exe);
        if(nm_download_file(burl,newp)!=0){ remove(newp); continue; }
        if(nm_filesize(newp)<20000){ if(verbose) fprintf(stderr,"update: download too small (error page?), trying next mirror\n"); remove(newp); continue; }
        if(want_sha[0]){
            char got[65]=""; int ok = (nm_file_sha256_hex(newp,got)==0);
            if(ok){ for(const char *x=got,*y=want_sha; ; x++,y++){ char cx=*x,cy=*y;
                if(cx>='A'&&cx<='Z')cx+=32; if(cy>='A'&&cy<='Z')cy+=32; if(cx!=cy){ok=0;break;} if(!cx)break; } }
            if(!ok){ if(verbose) fprintf(stderr,"update: sha256 mismatch - rejecting this mirror\n"); remove(newp); continue; }
        }
        char oldp[4112]; snprintf(oldp,sizeof oldp,"%s.old",exe); remove(oldp);
        if(rename(exe,oldp)!=0){ remove(newp); return -1; }
        if(rename(newp,exe)!=0){ rename(oldp,exe); remove(newp); return -1; }
#ifndef _WIN32
        chmod(exe,0755);
#endif
        fprintf(stderr,"update: installed v%s (backup at %s)\n",ver,oldp);
        return 1;
    }
    if(verbose) fprintf(stderr,"update: no mirror reachable\n");
    return -1;
}
static void nm_reexec(void){
    char exe[4096]; nm_exe_path(exe,sizeof exe); if(!exe[0]) return;
    fprintf(stderr,"update: restarting into the new version...\n");
#ifdef _WIN32
    _execv(exe,(const char* const*)g_argv);
#else
    execv(exe,g_argv);
#endif
}
static void *update_thread(void *arg){
    (void)arg;
    nm_sleep_ms(30000);                 /* let mining settle first */
    for(;;){
        if(nm_check_and_update(0)==1){ nm_reexec(); /* if exec fails, keep mining */ }
        for(int i=0;i<6*60;i++) nm_sleep_ms(60000);   /* re-check every ~6h */
    }
    return NULL;
}

/* ------------------------------ supervisor -------------------------------- */
/* Run the actual miner as a CHILD and respawn it forever if it ever stops
 * abnormally (crash / signal / OOM / unhandled fault). The user is NEVER
 * dropped: the failure is logged and mining resumes within seconds. A clean
 * exit (code 0) stops the loop. Returns 1 if it supervised (caller should
 * return), 0 if it could not start a child (caller then runs inline). The child
 * is marked with UNM_CHILD=1 so it skips the supervisor and mines directly. */
static int nm_run_supervised(void){
    if(!g_exe_path[0]) return 0;
#ifndef _WIN32
    for(;;){
        pid_t pid=fork();
        if(pid<0) return 0;                       /* cannot fork -> run inline */
        if(pid==0){ setenv("UNM_CHILD","1",1); execv(g_exe_path,g_argv); _exit(127); }
        int st=0; while(waitpid(pid,&st,0)<0 && errno==EINTR){}
        if(WIFEXITED(st) && WEXITSTATUS(st)==0) return 1;        /* clean stop */
        if(WIFSIGNALED(st))
            fprintf(stderr,"\n[UNM] miner hit a critical error (signal %d) - logged; restarting in 3s, leave it running\n",WTERMSIG(st));
        else
            fprintf(stderr,"\n[UNM] miner stopped (exit %d) - restarting in 3s, leave it running\n",WIFEXITED(st)?WEXITSTATUS(st):-1);
        nm_sleep_ms(3000);
    }
#else
    char *cl=GetCommandLineA();
    for(;;){
        SetEnvironmentVariableA("UNM_CHILD","1");
        STARTUPINFOA si; memset(&si,0,sizeof si); si.cb=sizeof si;
        PROCESS_INFORMATION pi; memset(&pi,0,sizeof pi);
        char cmd[8192]; snprintf(cmd,sizeof cmd,"%s",cl?cl:"");
        if(!CreateProcessA(g_exe_path,cmd,NULL,NULL,TRUE,0,NULL,NULL,&si,&pi)) return 0;
        WaitForSingleObject(pi.hProcess,INFINITE);
        DWORD ec=1; GetExitCodeProcess(pi.hProcess,&ec);
        CloseHandle(pi.hProcess); CloseHandle(pi.hThread);
        if(ec==0) return 1;
        fprintf(stderr,"\n[UNM] miner stopped (code %lu) - restarting in 3s, leave it running\n",(unsigned long)ec);
        nm_sleep_ms(3000);
    }
#endif
}

/* ----------------------------- main -------------------------------------- */

static int default_threads(void){
#ifdef _WIN32
    SYSTEM_INFO si; GetSystemInfo(&si); return (int)si.dwNumberOfProcessors;
#else
    long n=sysconf(_SC_NPROCESSORS_ONLN); return n>0?(int)n:4;
#endif
}

int main(int argc,char**argv){
    setvbuf(stdout,NULL,_IONBF,0);   /* show stats immediately even when piped */
    g_argv=argv;
    nm_exe_path(g_exe_path,sizeof g_exe_path);
#ifdef _WIN32
    WSADATA wsa; WSAStartup(MAKEWORD(2,2),&wsa);
#endif
    /* remove a leftover .old binary from a previous self-update */
    if(g_exe_path[0]){ char op[4112]; snprintf(op,sizeof op,"%s.old",g_exe_path); remove(op); }
    const char *node=NULL,*addr=NULL; int threads=0,lanes=1,bench=0,autol=0,use_smt=0,nonuma=0,do_update=0,noupdate=0;
    for(int i=1;i<argc;i++){
        if((!strcmp(argv[i],"-node")||!strcmp(argv[i],"-o")||!strcmp(argv[i],"--pool")||!strcmp(argv[i],"-url")||!strcmp(argv[i],"--url"))&&i+1<argc) node=argv[++i];
        else if((!strcmp(argv[i],"-addr")||!strcmp(argv[i],"-u")||!strcmp(argv[i],"--user")||!strcmp(argv[i],"--wallet"))&&i+1<argc) addr=argv[++i];
        else if(!strcmp(argv[i],"-threads")&&i+1<argc) threads=atoi(argv[++i]);
        else if(!strcmp(argv[i],"-lanes")&&i+1<argc){ const char*v=argv[++i]; if(!strcmp(v,"auto")) autol=1; else lanes=atoi(v); }
        else if(!strcmp(argv[i],"-smt")) use_smt=1;
        else if(!strcmp(argv[i],"-nonuma")) nonuma=1;
        else if(!strcmp(argv[i],"-update")||!strcmp(argv[i],"--update")) do_update=1;
        else if(!strcmp(argv[i],"-noupdate")||!strcmp(argv[i],"--no-update")) noupdate=1;
        else if((!strcmp(argv[i],"-p")||!strcmp(argv[i],"--pass"))&&i+1<argc) i++; /* password: accepted+ignored (XMRig-compat) */
        else if(!strcmp(argv[i],"-bench")&&i+1<argc) bench=atoi(argv[++i]);
        else if(!strcmp(argv[i],"-h")||!strcmp(argv[i],"--help")){
            printf("UNM (Ultra Native Miner) - fastest NeuroMorph CPU miner, auto-tunes to your CPU\n"
                   "  -o URL  (aliases: -node --pool)   pool/node (required). Stratum is the default:\n"
                   "      -o stratum+tcp://stratum.cereblix.com:3333   (pool)\n"
                   "      -o stratum+tcp://stratum.cereblix.com:3334   (solo)\n"
                   "      -o ru.cereblix.com:3333    (bare host:port = stratum; .ru/.us/.asia regions)\n"
                   "      -o https://cereblix.com/pool/api   (legacy HTTP getwork; needs curl on PATH)\n"
                   "  -u crb1...  (aliases: -addr --wallet)   your CRB payout address (required)\n"
                   "  Auto by default: one worker per PHYSICAL core, spread across all NUMA nodes,\n"
                   "  a node-local 64 MiB dataset each, and the fastest AES-NI/VAES-256/VAES-512 fill\n"
                   "  (CPUID-detected + benchmarked + byte-verified). Overrides:\n"
                   "  -threads N                   cap to N workers (default: all physical cores)\n"
                   "  -smt                         also use SMT/Hyper-Threading siblings (usually slower)\n"
                   "  -nonuma                      one shared dataset (disable per-node replication)\n"
                   "  -lanes K | auto              interleaved nonce-lanes/thread (default 1)\n"
                   "  -bench SECONDS               offline benchmark, no network\n"
                   "  -update                      check for a newer signed release, install, exit\n"
                   "  -noupdate                    disable the background auto-update (or drop a\n"
                   "                               .noupdate file next to the binary)\n"
                   "  env NM_FILL=aesni|vaes256|vaes512, NM_NO_VAES=1   force a fill path (A/B)\n");
            return 0;
        }
    }
    if(lanes<1) lanes=1; if(lanes>NM_FAST_MAXLANES) lanes=NM_FAST_MAXLANES;
    if(do_update){ printf("nmminer v%s - checking for updates...\n",NM_VERSION); int r=nm_check_and_update(1); return r<0?1:0; }
    if(bench>0){ plan_workers(use_smt,threads,nonuma); if(autol) lanes=autotune_lanes(8); g_lanes=lanes; return run_bench(bench); }
    if(!node||!addr){ fprintf(stderr,"usage: nmminer -node URL -addr crb1... [-threads N] [-smt] [-nonuma] [-lanes K|auto]\n       URL e.g. https://cereblix.com/pool/api (pool) | https://cereblix.com/api (solo) | http://NODE_IP:18751/api\n       nmminer -bench SECONDS [-threads N] [-lanes K]\n"); return 1; }
    if(!nm_valid_addr(addr)){ fprintf(stderr,"error: invalid CRB address (expect crb1 + 40 hex)\n"); return 1; }
    if(parse_node(node)) return 1;
    if(!g_is_stratum){
        g_have_curl = curl_available();
        if(g_use_curl && !g_have_curl){
            fprintf(stderr,"error: this is an https:// URL but 'curl' was not found on PATH.\n"
                           "  nmminer uses curl for TLS getwork. Install curl, use a stratum URL\n"
                           "  (e.g. stratum+tcp://stratum.cereblix.com:3333), or a plain-HTTP node.\n");
            return 1;
        }
    }
    snprintf(g_addr,sizeof g_addr,"%s",addr);
    /* keep-alive supervisor: the top-level process respawns the miner forever if
     * it ever crashes, so the user is never dropped. The child (UNM_CHILD) mines. */
    if(!getenv("UNM_CHILD") && nm_run_supervised()) return 0;
    plan_workers(use_smt,threads,nonuma);
    if(autol) lanes=autotune_lanes(8);
    g_lanes=lanes;

    const char *transport = g_is_stratum ? "stratum" : (g_use_curl?"https getwork":"http getwork");
    printf("UNM (Ultra Native Miner) v%s | %s (%s) | addr %s\n  %d workers across %d NUMA node(s) | phys %d / log %d%s | lanes %d | fill %s\n",
           NM_VERSION, g_base, transport, addr,
           g_nworker,g_nnodes,g_nphysical,g_nlogical,use_smt?" (SMT on)":"",lanes,nm_fast_fill_name());

    pthread_t ft;
    if(g_is_stratum) pthread_create(&ft,NULL,stratum_thread,NULL);
    else             pthread_create(&ft,NULL,fetch_thread,NULL);
    /* wait briefly for first work so threads start with an epoch */
    for(int i=0;i<50 && !g_have_work;i++) nm_sleep_ms(100);

    int N=g_nworker;
    targ *ta=calloc(N,sizeof *ta); pthread_t *th=calloc(N,sizeof *th);
    for(int i=0;i<N;i++){ ta[i].id=i; ta[i].cpu=g_worker[i].cpu; ta[i].node=g_worker[i].node; pthread_create(&th[i],NULL,mine_thread,&ta[i]); }

    /* background self-update: forward-only, sha256-verified, opt out with -noupdate */
    if(!noupdate && !nm_noupdate_optout()){ pthread_t ut; pthread_create(&ut,NULL,update_thread,NULL); }

    uint64_t last=0; time_t t0=time(NULL);
    for(;;){
        nm_sleep_ms(15000);
        uint64_t cur=atomic_load(&g_hashes);
        double hs=(double)(cur-last)/15.0; last=cur;
        printf("hashrate: %.0f H/s | block %llu (epoch %llu) | shares %llu blocks %llu | up %llus\n",
               hs,(unsigned long long)g_height,(unsigned long long)g_epoch_no,
               (unsigned long long)atomic_load(&g_shares),(unsigned long long)atomic_load(&g_blocks),
               (unsigned long long)(time(NULL)-t0));
    }
    return 0;
}
