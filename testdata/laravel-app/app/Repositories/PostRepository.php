<?php

namespace App\Repositories;

use App\Models\Post;
use App\Traits\Userable;

final class PostRepository
{
    use Userable;

    public function published()
    {
        return $this->user->posts()->latest()->published()->get();
    }

    public function popular()
    {
        return Post::popular()->get();
    }

    public function ownerEmail(): string
    {
        return $this->user->email;
    }
}
