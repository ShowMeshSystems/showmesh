export declare const SRC_ROOT: string
export declare const WORDS_FILE: string
export declare const BASELINE_FILE: string
export declare function readBannedWords(file?: string): string[]
export declare function findBannedWords(
  source: string,
  fileName: string,
  words?: string[],
): { word: string; line: number; text: string }[]
export declare function countBannedWords(root?: string, words?: string[]): Map<string, { count: number; uses: string[] }>
export declare function readBaseline(file?: string): Map<string, number>
export declare function checkCopyWords(counts?: Map<string, { count: number; uses: string[] }>, baseline?: Map<string, number>): string[]
