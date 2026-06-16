import * as React from "react"
import { cn } from "../../lib/utils"

// Placeholder pulsante para estados de carregamento.
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-muted/60", className)}
      {...props}
    />
  )
}

export { Skeleton }
