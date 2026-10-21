import { AutoComplete } from 'antd';
import type { AutoCompleteProps } from 'antd';

import { useFrontOptions } from '@/api/queries/useRelaysQuery';
import { frontShareOptions } from '@/schemas/relay';

// The share address box. It suggests the entry servers this panel has
// enrolled, so an inbound behind one hands its customers the front's name in
// every share link, cisco login and OpenVPN profile; those all read this one
// address. Anything else can still be typed, as before.
export function ShareAddrInput(props: AutoCompleteProps) {
  const { fronts } = useFrontOptions();
  const options = frontShareOptions(fronts);
  return (
    <AutoComplete
      placeholder="edge.example.com"
      {...props}
      options={options}
      filterOption={(input, option) => {
        const needle = input.toLowerCase();
        return (
          String(option?.value ?? '')
            .toLowerCase()
            .includes(needle) ||
          String(option?.label ?? '')
            .toLowerCase()
            .includes(needle)
        );
      }}
    />
  );
}
