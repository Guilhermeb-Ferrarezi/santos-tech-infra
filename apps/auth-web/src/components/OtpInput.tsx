import { useRef } from 'react'

interface OtpInputProps {
  value: string
  onChange: (val: string) => void
  length?: number
  autoFocus?: boolean
}

export default function OtpInput({ value, onChange, length = 6, autoFocus = false }: OtpInputProps) {
  const refs = useRef<(HTMLInputElement | null)[]>([])

  function focus(idx: number) {
    refs.current[idx]?.focus()
    refs.current[idx]?.select()
  }

  function handleChange(idx: number, raw: string) {
    const char = raw.replace(/[^0-9A-Z]/gi, '').toUpperCase().slice(0, 1)
    if (!char) return
    const next = value.slice(0, idx) + char + value.slice(idx + 1)
    onChange(next)
    if (idx < length - 1) focus(idx + 1)
  }

  function handleKeyDown(idx: number, e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Backspace') {
      e.preventDefault()
      if (value[idx]) {
        onChange(value.slice(0, idx) + value.slice(idx + 1))
      } else if (idx > 0) {
        onChange(value.slice(0, idx - 1) + value.slice(idx))
        focus(idx - 1)
      }
    } else if (e.key === 'ArrowLeft' && idx > 0) {
      e.preventDefault()
      focus(idx - 1)
    } else if (e.key === 'ArrowRight' && idx < length - 1) {
      e.preventDefault()
      focus(idx + 1)
    }
  }

  function handlePaste(e: React.ClipboardEvent) {
    e.preventDefault()
    const pasted = e.clipboardData.getData('text')
      .replace(/[^0-9A-Z]/gi, '').toUpperCase().slice(0, length)
    onChange(pasted)
    setTimeout(() => focus(Math.min(pasted.length, length - 1)), 0)
  }

  return (
    <div className="flex gap-2 justify-center" role="group" aria-label="Código de verificação">
      {Array.from({ length }, (_, i) => (
        <input
          key={i}
          ref={el => { refs.current[i] = el }}
          type="text"
          inputMode="numeric"
          autoComplete={i === 0 ? 'one-time-code' : 'off'}
          autoFocus={autoFocus && i === 0}
          maxLength={2}
          value={value[i] ?? ''}
          onChange={e => handleChange(i, e.target.value)}
          onKeyDown={e => handleKeyDown(i, e)}
          onPaste={handlePaste}
          onFocus={e => e.target.select()}
          className="w-11 h-14 border border-gray-200 rounded-xl bg-[#F5F8FA] text-center text-2xl font-mono font-semibold text-[#0E2937] focus:outline-none focus:border-[#187ABF] focus:bg-white transition-colors caret-transparent"
        />
      ))}
    </div>
  )
}
