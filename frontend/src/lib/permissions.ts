import type { MenuProps } from 'antd';

type MenuItems = NonNullable<MenuProps['items']>;

// Drops menu entries whose key maps to a switch the caller lacks. Entries with
// no mapping (dividers, read-only actions) are kept. The backend refuses the
// same actions; this only stops the panel from offering doors that will not open.
export function filterMenuByPermission(
  items: MenuItems,
  can: (permission: string) => boolean,
  permissionByKey: Record<string, string>,
): MenuItems {
  return items.filter((item) => {
    if (!item || !('key' in item) || typeof item.key !== 'string') return true;
    const needed = permissionByKey[item.key];
    return !needed || can(needed);
  });
}
