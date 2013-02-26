#include "runtime.h"
#include "defs.h"
#include "os.h"
#include "malloc.h"

enum
{
	ENOMEM = 12,
};

static int32
addrspace_free(void *v, uintptr n)
{
	uintptr page_size = 4096;
	uintptr off;
	int8 one_byte;

	for(off = 0; off < n; off += page_size) {
		int32 errval = runtime·mincore((int8 *)v + off, page_size, (void *)&one_byte);
		// errval is 0 if success, or -(error_code) if error.
		if (errval == 0 || errval != -ENOMEM)
			return 0;
	}
	USED(v);
	USED(n);
	return 1;
}


void*
runtime·SysAlloc(uintptr n)
{
	void *p;
	p = nil;
	mstats.sys += n;
	runtime·mmap((void*)&p, n, MMAP_WRITE, 0, 0, 0);
	runtime·memclr(p, n);
	return p;
}

void
runtime·SysUnused(void *v, uintptr n)
{
	USED(v);
	USED(n);
	// TODO(rsc): call madvise MADV_DONTNEED
}

void
runtime·SysFree(void *v, uintptr n)
{
	mstats.sys -= n;
	runtime·munmap(v, n);
}

void*
runtime·SysReserve(void *v, uintptr n)
{
	// On 64-bit, people with ulimit -v set complain if we reserve too
	// much address space.  Instead, assume that the reservation is okay
	// and check the assumption in SysMap.
	if(sizeof(void*) == 8)
		return v;
	
	void *p = v;

	runtime·mmap((void*)&p, n, MMAP_WRITE, 0, 0, 0);
	if(p < (void*)4096) {
		return nil;
	}
	return p;
}

void
runtime·SysMap(void *v, uintptr n)
{
	void *p = v;
	
	mstats.sys += n;

	// On 64-bit, we don't actually have v reserved, so tread carefully.
	if(sizeof(void*) == 8) {
		runtime·mmap((void*)&p, n, MMAP_WRITE, 0, 0, 0);
		runtime·memclr(p, n);
		return;
	}

	runtime·mmap((void*)&p, n, MMAP_WRITE, 0, 0, 0);
	runtime·memclr(p, n);
}
