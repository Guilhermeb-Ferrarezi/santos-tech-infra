import BrandPanel from './BrandPanel'

export default function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="min-h-screen flex bg-white">
      {/* Header mobile */}
      <div
        className="md:hidden fixed top-0 left-0 right-0 z-10 flex items-center gap-3 px-5 py-4"
        style={{ background: 'linear-gradient(135deg, #0E2937 0%, #187ABF 100%)' }}
      >
        <img src="/logo.png" alt="Santos Tech" className="w-8 h-8 object-contain" />
        <span className="text-white font-bold text-base">Santos Tech</span>
      </div>

      <BrandPanel />

      <main className="flex-1 flex flex-col min-h-screen">
        <div className="flex-1 flex items-center justify-center p-8 pt-24 md:pt-8">
          <div className="w-full max-w-sm">
            {children}
          </div>
        </div>
        <footer className="text-center text-xs text-gray-400 pb-6">
          © {new Date().getFullYear()} Santos Tech · {' '}
          <a href="#" className="hover:underline">Termos de uso</a>
          {' · '}
          <a href="#" className="hover:underline">Privacidade</a>
        </footer>
      </main>
    </div>
  )
}
