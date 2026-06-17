import * as React from "react"
import { cn } from "../../lib/utils"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      className={cn(
        "flex h-14 w-full rounded-lg border border-[#e3eaf0] bg-[#f5f8fa] px-4 py-3 text-base transition-colors",
        "placeholder:text-muted-foreground/60",
        "focus-visible:border-[#0db88f] focus-visible:bg-white focus-visible:ring-2 focus-visible:ring-[#0db88f]/20 focus-visible:outline-none",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "file:border-0 file:bg-transparent file:text-sm file:font-medium",
        className
      )}
      {...props}
    />
  )
}

export { Input }
