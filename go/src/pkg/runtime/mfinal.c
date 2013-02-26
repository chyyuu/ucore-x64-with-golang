// Copyright 2010 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "runtime.h"
#include "malloc.h"

// Lock to protect finalizer data structures.
// Cannot reuse mheap.Lock because the finalizer
// maintenance requires allocation.
static Lock finlock;

// Finalizer hash table.  Direct hash, linear scan, at most 3/4 full.
// Table size is power of 3 so that hash can be key % max.
// Key[i] == (void*)-1 denotes free but formerly occupied entry
// (doesn't stop the linear scan).
// Key and val are separate tables because the garbage collector
// must be instructed to ignore the pointers in key but follow the
// pointers in val.
typedef struct Fintab Fintab;
struct Fintab
{
	void **key;
	Finalizer **val;
	int32 nkey;	// number of non-nil entries in key
	int32 ndead;	// number of dead (-1) entries in key
	int32 max;	// size of key, val allocations
};

static void
addfintab(Fintab *t, void *k, Finalizer *v)
{
	int32 i, j;

	i = (uintptr)k % (uintptr)t->max;
	for(j=0; j<t->max; j++) {
		if(t->key[i] == nil) {
			t->nkey++;
			goto ret;
		}
		if(t->key[i] == (void*)-1) {
			t->ndead--;
			goto ret;
		}
		if(++i == t->max)
			i = 0;
	}

	// cannot happen - table is known to be non-full
	runtime·throw("finalizer table inconsistent");

ret:
	t->key[i] = k;
	t->val[i] = v;
}

static Finalizer*
lookfintab(Fintab *t, void *k, bool del)
{
	int32 i, j;
	Finalizer *v;

	if(t->max == 0)
		return nil;
	i = (uintptr)k % (uintptr)t->max;
	for(j=0; j<t->max; j++) {
		if(t->key[i] == nil)
			return nil;
		if(t->key[i] == k) {
			v = t->val[i];
			if(del) {
				t->key[i] = (void*)-1;
				t->val[i] = nil;
				t->ndead++;
			}
			return v;
		}
		if(++i == t->max)
			i = 0;
	}

	// cannot happen - table is known to be non-full
	runtime·throw("finalizer table inconsistent");
	return nil;
}

static Fintab fintab;

// add finalizer; caller is responsible for making sure not already in table
void
runtime·addfinalizer(void *p, void (*f)(void*), int32 nret)
{
	Fintab newtab;
	int32 i;
	byte *base;
	Finalizer *e;
	
	e = nil;
	if(f != nil) {
		e = runtime·mal(sizeof *e);
		e->fn = f;
		e->nret = nret;
	}

	runtime·lock(&finlock);
	if(!runtime·mlookup(p, &base, nil, nil) || p != base) {
		runtime·unlock(&finlock);
		runtime·throw("addfinalizer on invalid pointer");
	}
	if(f == nil) {
		lookfintab(&fintab, p, 1);
		runtime·unlock(&finlock);
		return;
	}

	if(lookfintab(&fintab, p, 0)) {
		runtime·unlock(&finlock);
		runtime·throw("double finalizer");
	}
	runtime·setblockspecial(p);

	if(fintab.nkey >= fintab.max/2+fintab.max/4) {
		// keep table at most 3/4 full:
		// allocate new table and rehash.

		runtime·memclr((byte*)&newtab, sizeof newtab);
		newtab.max = fintab.max;
		if(newtab.max == 0)
			newtab.max = 3*3*3;
		else if(fintab.ndead < fintab.nkey/2) {
			// grow table if not many dead values.
			// otherwise just rehash into table of same size.
			newtab.max *= 3;
		}

		newtab.key = runtime·mallocgc(newtab.max*sizeof newtab.key[0], FlagNoPointers, 0, 1);
		newtab.val = runtime·mallocgc(newtab.max*sizeof newtab.val[0], 0, 0, 1);

		for(i=0; i<fintab.max; i++) {
			void *k;

			k = fintab.key[i];
			if(k != nil && k != (void*)-1)
				addfintab(&newtab, k, fintab.val[i]);
		}
		runtime·free(fintab.key);
		runtime·free(fintab.val);
		fintab = newtab;
	}

	addfintab(&fintab, p, e);
	runtime·unlock(&finlock);
}

// get finalizer; if del, delete finalizer.
// caller is responsible for updating RefHasFinalizer bit.
Finalizer*
runtime·getfinalizer(void *p, bool del)
{
	Finalizer *f;
	
	runtime·lock(&finlock);
	f = lookfintab(&fintab, p, del);
	runtime·unlock(&finlock);
	return f;
}

void
runtime·walkfintab(void (*fn)(void*))
{
	void **key;
	void **ekey;

	runtime·lock(&finlock);
	key = fintab.key;
	ekey = key + fintab.max;
	for(; key < ekey; key++)
		if(*key != nil && *key != ((void*)-1))
			fn(*key);
	runtime·unlock(&finlock);
}
