import { useState } from 'react';

export interface OverlayOpenArgs<T extends { [prop: string]: any }> {
  open: boolean;
  args: T;
}

export const useOverlay = <T extends { [prop: string]: any }>() => {
  const [openArgs, setOpenArgs] = useState<OverlayOpenArgs<T>>({
    open: false,
    args: {} as T,
  });

  const setOpen = (open: boolean) => {
    setOpenArgs((prev) => ({ ...prev, open }));
  };

  return {
    openArgs,
    setOpen,
    setOpenArgs,
  };
};
