import { landingHref } from '../links'
import { Button, Logo } from '../ds'

export function Header() {
  return (
    <header className="check-head">
      <div className="check-wrap check-head-row">
        <a href={landingHref('/')} aria-label="ASComply Africa home" className="check-focus check-logo-link">
          <Logo size={30} />
        </a>
        <nav aria-label="Page" className="check-nav">
          <a href="#what" className="check-navlink check-nav-desk check-focus">
            What you get
          </a>
          <a href={landingHref('/')} className="check-navlink check-nav-desk check-focus">
            Platform
          </a>
          <Button variant="outline" size="sm" href={landingHref('/?register')}>
            Sign up
          </Button>
        </nav>
      </div>
    </header>
  )
}
