// Each reseller's clients are stored under an "a<adminId>:" namespace, so two
// resellers can both have a customer called "ali". Every request still uses
// the stored value; only what the reader sees is stripped.
const OWNER_PREFIX = /^a\d+:/;

export function displayEmail(email: string): string {
  return email.replace(OWNER_PREFIX, '');
}

export function hasOwnerPrefix(email: string): boolean {
  return OWNER_PREFIX.test(email);
}
