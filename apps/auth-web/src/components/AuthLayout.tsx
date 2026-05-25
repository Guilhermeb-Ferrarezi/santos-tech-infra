import BrandPanel from './BrandPanel'

export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen flex bg-white">
      <BrandPanel />
      <main className="flex-1 flex items-center justify-center p-8">
        <div className="w-full max-w-sm">
          {children}
        </div>
      </main>
    </div>
  )
}
