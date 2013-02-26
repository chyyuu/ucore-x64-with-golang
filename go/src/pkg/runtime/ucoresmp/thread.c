// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "runtime.h"
#include "defs.h"
#include "os.h"
#include "stack.h"

extern SigTab runtime·sigtab[];
static int32 proccount;

int32 runtime·open(uint8*, int32, int32);
int32 runtime·close(int32);
int32 runtime·read(int32, void*, int32);

enum
{
	MUTEX_UNLOCKED = 0,
	MUTEX_LOCKED = 1,
	MUTEX_SLEEPING = 2,

	ACTIVE_SPIN = 4,
	ACTIVE_SPIN_CNT = 30,
	PASSIVE_SPIN = 1,

	FUTEX_WAIT = 0,
	FUTEX_WAKE = 1,

	EINTR = 4,
	EAGAIN = 11,
};

// TODO(rsc): I tried using 1<<40 here but futex woke up (-ETIMEDOUT).
// I wonder if the timespec that gets to the kernel
// actually has two 32-bit numbers in it, so that
// a 64-bit 1<<40 ends up being 0 seconds,
// 1<<8 nanoseconds.
static Timespec longtime =
{
	1<<30,	// 34 years
	0
};

// Thread-safe allocation of a semaphore.
// Psema points at a kernel semaphore key.
// It starts out zero, meaning no semaphore.
// Fill it in, being careful of others calling initsema
// simultaneously.
static void
initsema(uint32 *psema, uint32 value)
{
	uint32 sema;

	if(*psema != 0)	// already have one
		return;

	sema = runtime·sem_init(value);
	
	// [MARK-PETER] runtime.cas:
	// if (*psema == 0) {
	//   *psema = sema;
	//   return true;
	// }
	// else return false;
	if(!runtime·cas(psema, 0, sema)){
		// Someone else filled it in.  Use theirs.
		runtime·sem_free(sema);
		return;
	}
}

static int32
getproccount(void)
{
	int32 fd, rd, cnt, cpustrlen;
	byte *cpustr, *pos, *bufpos;
	byte buf[256];

	fd = runtime·open((byte*)"/proc/stat", O_RDONLY|O_CLOEXEC, 0);
	if(fd == -1)
		return 1;
	cnt = 0;
	bufpos = buf;
	cpustr = (byte*)"\ncpu";
	cpustrlen = runtime·findnull(cpustr);
	for(;;) {
		rd = runtime·read(fd, bufpos, sizeof(buf)-cpustrlen);
		if(rd == -1)
			break;
		bufpos[rd] = 0;
		for(pos=buf; pos=runtime·strstr(pos, cpustr); cnt++, pos++) {
		}
		if(rd < cpustrlen)
			break;
		runtime·memmove(buf, bufpos+rd-cpustrlen+1, cpustrlen-1);
		bufpos = buf+cpustrlen-1;
	}
	runtime·close(fd);
	return cnt ? cnt : 1;
}

void
runtime·lock(Lock *l)
{
	if(m->locks < 0)
		runtime·throw("lock count");
	m->locks++;
	
	if(runtime·xadd(&l->key, 1) > 1) {	// someone else has it; wait
		// Allocate semaphore if needed.
		if(l->sema == 0)
			initsema(&l->sema, 0);
		runtime·sem_wait(l->sema, 0);
	}
}

void
runtime·unlock(Lock *l)
{
	m->locks--;
	if(m->locks < 0)
		runtime·throw("lock count");
	
	if(runtime·xadd(&l->key, -1) > 0) {	// someone else is waiting
		// Allocate semaphore if needed.
		if(l->sema == 0)
			initsema(&l->sema, 0);
		runtime·sem_post(l->sema);
	}
}

// User-level semaphore implementation:
// try to do the operations in user space on u,
// but when it's time to block, fall back on the kernel semaphore k.
// This is the same algorithm used in Plan 9.
void
runtime·usemacquire(Usema *s)
{
	if((int32)runtime·xadd(&s->u, -1) < 0) {
		if(s->k == 0)
			initsema(&s->k, 0);
		runtime·sem_wait(s->k, 0);
	}
}

void
runtime·usemrelease(Usema *s)
{
	if((int32)runtime·xadd(&s->u, 1) <= 0) {
		if(s->k == 0)
			initsema(&s->k, 0);
		runtime·sem_post(s->k);
	}
}

// One-time notifications.
void
runtime·noteclear(Note *n)
{
	n->state = 0;
}

void
runtime·notewakeup(Note *n)
{
	n->wakeup = 1;
	runtime·usemrelease(&n->sema);
}

void
runtime·notesleep(Note *n)
{
	while(!n->wakeup)
		runtime·usemacquire(&n->sema);
}


enum
{
	CLONE_VM        =   0x00000100,  // set if VM shared between processes
	CLONE_THREAD    =   0x00000200,  // thread group
	CLONE_SEM       =   0x00000400,  // set if shared between processes
	CLONE_FS        =   0x00000800,  // set if shared between processes
};

void
runtime·newosproc(M *m, G *g, void *stk, void (*fn)(void))
{
	int32 ret;
	int32 flags;

	/*
	 * note: strace gets confused if we use CLONE_PTRACE here.
	 */
	flags = CLONE_VM | CLONE_FS | CLONE_SEM | CLONE_THREAD;

	m->tls[0] = m->id;	// so 386 asm can find it
	if(0){
		runtime·printf("newosproc stk=%p m=%p g=%p fn=%p clone=%p id=%d/%d ostk=%p\n",
			stk, m, g, fn, runtime·clone, m->id, m->tls[0], &m);
	}

	if((ret = runtime·clone(flags, stk, m, g, fn)) < 0) {
		runtime·printf("runtime: failed to create new OS thread (have %d already; errno=%d)\n", runtime·mcount(), -ret);
		runtime·throw("runtime.newosproc");
	}
}

void
runtime·osinit(void)
{
}

void
runtime·goenvs(void)
{
	runtime·goenvs_ucoresmp();
}

// Called to initialize a new m (including the bootstrap m).
void
runtime·minit(void)
{
	// Initialize signal handling.
	m->gsignal = runtime·malg(32*1024);	// OS X wants >=8K, Linux >=2K
	runtime·signalstack(m->gsignal->stackguard - StackGuard, 32*1024);
}

void
runtime·sigpanic(void)
{
	switch(g->sig) {
	case SIGBUS:
		if(g->sigcode0 == BUS_ADRERR && g->sigcode1 < 0x1000)
			runtime·panicstring("invalid memory address or nil pointer dereference");
		runtime·printf("unexpected fault address %p\n", g->sigcode1);
		runtime·throw("fault");
	case SIGSEGV:
		if((g->sigcode0 == 0 || g->sigcode0 == SEGV_MAPERR || g->sigcode0 == SEGV_ACCERR) && g->sigcode1 < 0x1000)
			runtime·panicstring("invalid memory address or nil pointer dereference");
		runtime·printf("unexpected fault address %p\n", g->sigcode1);
		runtime·throw("fault");
	case SIGFPE:
		switch(g->sigcode0) {
		case FPE_INTDIV:
			runtime·panicstring("integer divide by zero");
		case FPE_INTOVF:
			runtime·panicstring("integer overflow");
		}
		runtime·panicstring("floating point error");
	}
	runtime·panicstring(runtime·sigtab[g->sig].name);
}
