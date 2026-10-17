import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../lib/cn";

const buttonVariants = cva("button", {
  variants: {
    tone: {
      default: "button-default",
      danger: "button-danger",
    },
  },
  defaultVariants: { tone: "default" },
});

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & { asChild?: boolean };

export function Button({
  asChild = false,
  tone,
  className,
  type = "button",
  ...props
}: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return (
    <Comp
      className={cn(buttonVariants({ tone }), className)}
      type={asChild ? undefined : type}
      {...props}
    />
  );
}
