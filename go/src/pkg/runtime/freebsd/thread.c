// Use of this source file is governed by a BSD-style
// license that can be found in the LICENSE file.`

#include "runtime.h"
#include "defs.h"
#include "os.h"
#include "stack.h"

extern SigTab runtime·sigtab[];
extern int32 runtime·sys_umtx_op(uint32*, int32, uint32, void*, void*);

// FreeBSD's umtx_op syscall is effectively the same as Linux's futex, and
// thus the code is largely similar. See linux/thread.c for comments.

static void
umtx_wait(uint32 *addr, uint32 val)
{
	int32 ret;

	ret = runtime·sys_umtx_op(addr, UMTX_OP_WAIT, val, nil, nil);
	if(ret >= 0 || ret == -EINTR)
		return;

	runtime·printf("umtx_wait addr=%p val=%d ret=%d\n", addr, val, ret);
	*(int32*)0x1005 = 0x1005;
}

static void
umtx_wake(uint32 *addr)
{
	int32 ret;

	ret = runtime·sys_umtx_op(addr, UMTX_OP_WAKE, 1, nil, nil);
	if(ret >= 0)
		return;

	runtime·printf("umtx_wake addr=%p ret=%d\n", addr, ret);
	*(int32*)0x1006 = 0x1006;
}

// See linux/thread.c for comments about the algorithm.
static void
umtx_lock(Lock *l)
{
	uint32 v;

again:
	v = l->key;
	if((v&1) == 0){
		if(runtime·cas(&l->key, v, v|1))
			return;
		goto again;
	}

	if(!runtime·cas(&l->key, v, v+2))
		goto again;

	umtx_wait(&l->key, v+2);

	for(;;){
		v = l->key;
		if(v < 2)
			runtime·throw("bad lock key");
		if(runtime·cas(&l->key, v, v-2))
			break;
	}

	goto again;
}

static void
umtx_unlock(Lock *l)
{
	uint32 v;

again:
	v = l->key;
	if((v&1) == 0)
		runtime·throw("unlock of unlocked lock");
	if(!runtime·cas(&l->key, v, v&~1))
		goto again;

	if(v&~1)
		umtx_wake(&l->key);
}

void
runtime·lock(Lock *l)
{
	if(m->locks < 0)
		runtime·throw("lock count");
	m->locks++;
	umtx_lock(l);
}

void 
runtime·unlock(Lock *l)
{
	m->locks--;
	if(m->locks < 0)
		runtime·throw("lock count");
	umtx_unlock(l);
}

// Event notifications.
void
runtime·noteclear(Note *n)
{
	n->lock.key = 0;
	umtx_lock(&n->lock);
}

void
runtime·notesleep(Note *n)
{
	umtx_lock(&n->lock);
	umtx_unlock(&n->lock);
}

void
runtime·notewakeup(Note *n)
{
	umtx_unlock(&n->lock);
}

void runtime·thr_start(void*);

void
runtime·newosproc(M *m, G *g, void *stk, void (*fn)(void))
{
	ThrParam param;

	USED(fn);	// thr_start assumes fn == mstart
	USED(g);	// thr_start assumes g == m->g0

	if(0){
		runtime·printf("newosproc stk=%p m=%p g=%p fn=%p id=%d/%d ostk=%p\n",
			stk, m, g, fn, m->id, m->tls[0], &m);
	}

	runtime·memclr((byte*)&param, sizeof param);

	param.start_func = runtime·thr_start;
	param.arg = m;
	param.stack_base = (int8*)g->stackbase;
	param.stack_size = (byte*)stk - (byte*)g->stackbase;
	param.child_tid = (intptr*)&m->procid;
	param.parent_tid = nil;
	param.tls_base = (int8*)&m->tls[0];
	param.tls_size = sizeof m->tls;

	m->tls[0] = m->id;	// so 386 asm can find it

	runtime·thr_new(&param, sizeof param);
}

void
runtime·osinit(void)
{
}

void
runtime·goenvs(void)
{
	runtime·goenvs_unix();
}

// Called to initialize a new m (including the bootstrap m).
void
runtime·minit(void)
{
	// Initialize signal handling
	m->gsignal = runtime·malg(32*1024);
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
