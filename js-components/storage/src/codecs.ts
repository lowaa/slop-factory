// A Codec turns a value into stored text and back. decode is undefined for
// text it does not recognise, which readers treat as "nothing stored".
export interface Codec<T> {
  encode(value: T): string;
  decode(text: string): T | undefined;
}

// boolCodec keeps a boolean as the given texts, so a value written before
// this package existed ("open"/"closed", "1"/"0") still reads.
export function boolCodec(trueText: string, falseText: string): Codec<boolean> {
  return {
    encode: (value) => (value ? trueText : falseText),
    decode: (text) => (text === trueText ? true : text === falseText ? false : undefined),
  };
}

export function enumCodec<T extends string>(allowed: readonly T[]): Codec<T> {
  return {
    encode: (value) => value,
    decode: (text) => allowed.find((candidate) => candidate === text),
  };
}

// jsonCodec needs a guard: stored text is whatever a past version, another
// tab or the person wrote, so its shape is checked rather than asserted.
export function jsonCodec<T>(isValid: (parsed: unknown) => parsed is T): Codec<T> {
  return {
    encode: (value) => JSON.stringify(value),
    decode: (text) => {
      try {
        const parsed: unknown = JSON.parse(text);
        return isValid(parsed) ? parsed : undefined;
      } catch {
        return undefined;
      }
    },
  };
}
