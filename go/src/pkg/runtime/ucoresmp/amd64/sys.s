// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//
// System calls and other sys.stuff for AMD64, Linux
//

#include "amd64/asm.h"

TEXT runtime·exit(SB),7,$0-8
	MOVL	8(SP), DI
	MOVL	$1, AX	// exitgroup - force all os threads to exit
	INT $0x80
	RET

TEXT runtime·exit1(SB),7,$0-8
	MOVL	8(SP), DI
	MOVL	$1, AX	// exit - exit the current os thread
	INT $0x80
	RET

TEXT runtime·open(SB),7,$0-16
	MOVQ	8(SP), DI
	MOVL	16(SP), SI
	MOVL	20(SP), DX
	MOVL	$2, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·close(SB),7,$0-16
	MOVL	8(SP), DI
	MOVL	$3, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·write(SB),7,$0-24
	MOVL	$1, DI //8(SP), DI
	MOVQ	16(SP), SI
	MOVL	24(SP), DX
	MOVL	$103, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·read(SB),7,$0-24
	MOVL	8(SP), DI
	MOVQ	16(SP), SI
	MOVL	24(SP), DX
	MOVL	$0, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·raisesigpipe(SB),7,$12
	MOVL	$186, AX	// syscall - gettid
	INT $0x80
	MOVL	AX, DI	// arg 1 tid
	MOVL	$13, SI	// arg 2 SIGPIPE
	MOVL	$200, AX	// syscall - tkill
	INT $0x80
	RET

TEXT runtime·setitimer(SB),7,$0-24
	MOVL	8(SP), DI
	MOVQ	16(SP), SI
	MOVQ	24(SP), DX
	MOVL	$38, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·mincore(SB),7,$0-24
	MOVQ	8(SP), DI
	MOVQ	16(SP), SI
	MOVQ	24(SP), DX
	MOVL	$27, AX			// syscall entry
	INT $0x80
	RET

TEXT runtime·gettime(SB), 7, $32
	MOVQ	8(SP), DI
	MOVQ	16(SP), SI
	MOVQ	$151, AX		// getpcstime
	INT $0x80
	RET

TEXT runtime·rt_sigaction(SB),7,$0-32
	RET

TEXT runtime·sigtramp(SB),7,$64
	get_tls(BX)

	// save g
	MOVQ	g(BX), R10
	MOVQ	R10, 40(SP)

	// g = m->gsignal
	MOVQ	m(BX), BP
	MOVQ	m_gsignal(BP), BP
	MOVQ	BP, g(BX)

	MOVQ	DI, 0(SP)
	MOVQ	SI, 8(SP)
	MOVQ	DX, 16(SP)
	MOVQ	R10, 24(SP)

	CALL	runtime·sighandler(SB)

	// restore g
	get_tls(BX)
	MOVQ	40(SP), R10
	MOVQ	R10, g(BX)
	RET

TEXT runtime·sigignore(SB),7,$0
	RET

TEXT runtime·sigreturn(SB),7,$0
	MOVL	$255, AX
	INT $0x80
	RET

TEXT runtime·mmap(SB),7,$0
	MOVQ	8(SP), DI
	MOVQ	$0, SI
	MOVQ	16(SP), SI
	MOVL	24(SP), DX
	MOVL	$20, AX			// mmap
	INT $0x80
	RET

TEXT runtime·munmap(SB),7,$0
	MOVQ	8(SP), DI
	MOVQ	16(SP), SI
	MOVQ	$91, AX	// munmap
	INT $0x80
	CMPQ	AX, $0xfffffffffffff001
	JLS	2(PC)
	CALL	runtime·notok(SB)
	RET

TEXT runtime·notok(SB),7,$0
	MOVQ	$0xf1, BP
	MOVQ	BP, (BP)
	RET

// uint32 runtime·sem_init(uint32 value)
TEXT runtime·sem_init(SB),7,$0
	MOVL	$40, AX		// sys_sem_init;
	MOVL	8(SP), DI
	INT	$0x80
	RET

// uint32 runtime·sem_post(uint32 sema)
TEXT runtime·sem_post(SB),7,$0
	MOVL	$41, AX		// sys_sem_post;
	MOVL	8(SP), DI
	INT $0X80
	RET

// uint32 runtime·sem_wait(uint32 sema, uint timeout)
TEXT runtime·sem_wait(SB),7,$0
	MOVL	$42, AX		// sys_sem_wait;
	MOVL	8(SP), DI
	MOVL	12(SP), SI
	INT $0X80
	RET

// uint32 runtime·sem_free(uint32 sema)
TEXT runtime·sem_free(SB),7,$0
	MOVL	$43, AX		// sys_sem_free;
	MOVL	8(SP), DI
	INT $0X80
	RET

// int64 clone(int32 flags, void *stack, M *m, G *g, void (*fn)(void));
TEXT runtime·clone(SB),7,$0
	MOVL	flags+8(SP), DI
	MOVQ	stack+16(SP), SI

	// Copy m, g, fn off parent stack for use by child.
	// Careful: Linux system call clobbers CX and R11.
	MOVQ	mm+24(SP), R8
	MOVQ	gg+32(SP), R9
	MOVQ	fn+40(SP), R12

	MOVL	$5, AX
	INT $0x80

	// In parent, return.
	CMPQ	AX, $0
	JEQ	2(PC)
	RET
	
	// In child, on new stack.
	MOVQ	SI, SP
	
	MOVL	$18, AX	// getpid
	INT $0x80
	MOVQ	AX, m_procid(R8)

	// Set FS to point at m->tls.
	LEAQ	m_tls(R8), DI
	CALL	runtime·settls(SB)

	// In child, set up new stack
	get_tls(CX)
	MOVQ	R8, m(CX)
	MOVQ	R9, g(CX)
	CALL	runtime·stackcheck(SB)

	// Call fn
	CALL	R12

	// It shouldn't return.  If it does, exit
	MOVL	$111, DI
	MOVL	$1, AX
	INT $0x80
	JMP	-3(PC)	// keep exiting

TEXT runtime·sigaltstack(SB),7,$-8
	MOVQ	$255, AX
	INT $0x80
	RET

// set tls base to DI
TEXT runtime·settls(SB),7,$32
	ADDQ	$16, DI	// ELF wants to use -16(FS), -8(FS)

	MOVQ	DI, SI
	MOVQ	$0x1, DI	// SET_FS
	MOVQ	$150, AX	// sys_prctl
	INT $0x80
	CMPQ	AX, $0xfffffffffffff001
	JLS	2(PC)
	CALL	runtime·notok(SB)
	RET

TEXT runtime·osyield(SB),7,$0
	MOVL	$24, AX
	INT $0x80
	RET
