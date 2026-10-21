import * as Dialog from "@radix-ui/react-dialog";
import { useLayoutEffect, useRef } from "react";
import { Button } from "./Button";

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  tone = "default",
  onConfirm,
  onOpenChange,
}: {
  open: boolean;
  title: string;
  description: string;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const openerRef = useRef<HTMLElement | null>(null);

  // Modal Dialog.Content preventDefaults close-auto-focus and then focuses
  // triggerRef. This dialog has no Dialog.Trigger, so that ref stays empty
  // and focus falls on body. Snapshot the opener before Radix moves focus in.
  useLayoutEffect(() => {
    if (!open) return;
    const active = document.activeElement;
    if (active instanceof HTMLElement && active !== document.body) {
      openerRef.current = active;
    }
  }, [open]);

  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          onCloseAutoFocus={(event) => {
            const opener = openerRef.current;
            openerRef.current = null;
            if (!opener?.isConnected) return;
            event.preventDefault();
            opener.focus();
          }}
        >
          <Dialog.Title>{title}</Dialog.Title>
          <Dialog.Description>{description}</Dialog.Description>
          <div className="dialog-actions">
            <Dialog.Close asChild>
              <Button type="button">{cancelLabel}</Button>
            </Dialog.Close>
            <Button type="button" tone={tone} onClick={onConfirm}>
              {confirmLabel}
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
