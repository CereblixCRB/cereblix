#ifndef CORE_ADDR_H
#define CORE_ADDR_H
#include <string.h>
/* Mirror of core.ValidAddr: "crb1" + exactly 40 hex chars. */
static inline int nm_valid_addr(const char *a){
    if(!a) return 0;
    if(strncmp(a,"crb1",4)!=0) return 0;
    if(strlen(a)!=44) return 0;
    for(int i=4;i<44;i++){ char c=a[i];
        if(!((c>='0'&&c<='9')||(c>='a'&&c<='f')||(c>='A'&&c<='F'))) return 0; }
    return 1;
}
#endif
