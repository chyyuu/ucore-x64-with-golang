/* Thanks to u10proj revision 72 */
OUTPUT_FORMAT("elf32-i386")
OUTPUT_ARCH(i386)
ENTRY(start)
SECTIONS {
 .text : {
 *(.text)
 }
 .data : {
 *(.data)
 }
 .bss : {
 *(.bss)
 }
 /DISCARD/ : {
 *(.eh_*)
 }
}
