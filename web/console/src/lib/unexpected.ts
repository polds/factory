export function unexpected(value: never): never {
  throw new Error(`Unexpected value: ${String(value)}`);
}
